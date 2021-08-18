package staticbackend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"staticbackend/internal"
	"staticbackend/middleware"
	"testing"
	"time"
)

// dbPost post on behalf of adminToken by default (use params[0] true for root)
func dbPost(t *testing.T, hf func(http.ResponseWriter, *http.Request), repo string, v interface{}, params ...bool) *http.Response {
	if params == nil {
		params = make([]bool, 0)
	}

	if len(params) == 0 {
		params = append(params, false)
	}

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal("error marshaling post data:", err)
	}

	req := httptest.NewRequest("POST", "/db/"+repo, bytes.NewReader(b))
	w := httptest.NewRecorder()

	req.Header.Set("SB-PUBLIC-KEY", pubKey)

	tok := adminToken
	if params[0] {
		tok = rootToken
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tok))

	stdAuth := []middleware.Middleware{
		middleware.WithDB(database.client, volatile),
		middleware.RequireAuth(database.client, volatile),
	}
	if params[0] {
		stdAuth = []middleware.Middleware{
			middleware.WithDB(client, volatile),
			middleware.RequireRoot(client),
		}
	}
	h := middleware.Chain(http.HandlerFunc(hf), stdAuth...)

	h.ServeHTTP(w, req)

	return w.Result()
}

func GetResponseBody(t *testing.T, resp *http.Response) string {
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("error reading response body: ", err)
	}

	return string(b)
}

func TestHasPermission(t *testing.T) {
	reads := make(map[string]internal.PermissionLevel)
	reads["tbl_740_"] = internal.PermGroup
	reads["tbl_600_"] = internal.PermOwner
	reads["tbl"] = internal.PermGroup
	reads["tbl_226_"] = internal.PermEveryone

	for k, v := range reads {
		if p := internal.ReadPermission(k); v != p {
			t.Errorf("%s expected read to be %v got %v", k, v, p)
		}
	}

	writes := make(map[string]internal.PermissionLevel)
	writes["tbl"] = internal.PermOwner
	writes["tbl_760_"] = internal.PermGroup
	writes["tbl_662_"] = internal.PermEveryone
	writes["tbl_244_"] = internal.PermOwner

	for k, v := range writes {
		if p := internal.WritePermission(k); v != p {
			t.Errorf("%s expected write to be %v got %v", k, v, p)
		}
	}
}

type Task struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Done    bool      `json:"done"`
	Created time.Time `json:"created"`
}

func TestDBCreate(t *testing.T) {
	task :=
		Task{
			Title:   "item created",
			Created: time.Now(),
		}

	resp := dbPost(t, database.add, "tasks", task)

	if resp.StatusCode > 299 {
		t.Fatal(GetResponseBody(t, resp))
	}

	var saved Task
	if err := parseBody(resp.Body, &saved); err != nil {
		t.Fatal(err)
	} else if task.Title != saved.Title {
		t.Errorf("expected title to be %s go %s", task.Title, saved.Title)
	}
}

func TestDBListCollections(t *testing.T) {
	req := httptest.NewRequest("GET", "/sudolistall", nil)
	w := httptest.NewRecorder()

	req.Header.Set("SB-PUBLIC-KEY", pubKey)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", rootToken))

	stdRoot := []middleware.Middleware{
		middleware.WithDB(database.client, volatile),
		middleware.RequireRoot(database.client),
	}
	h := middleware.Chain(http.HandlerFunc(database.listCollections), stdRoot...)

	h.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}

		t.Errorf("got error for list all collections: %s", string(b))
	}

	var names []string
	if err := parseBody(resp.Body, &names); err != nil {
		t.Fatal(err)
	} else if len(names) < 2 {
		t.Errorf("expected len to be > than 2 got %d", len(names))
	}
}
