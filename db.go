package staticbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"staticbackend/internal"
	"staticbackend/middleware"
	"strconv"
	"strings"
	"time"

	"staticbackend/cache"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Database struct {
	client *mongo.Client
	cache  *cache.Cache
}

func (database *Database) dbreq(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		database.add(w, r)
	} else if r.Method == http.MethodPut {
		database.update(w, r)
	} else if r.Method == http.MethodDelete {
		database.del(w, r)
	} else if r.Method == http.MethodGet {
		p := r.URL.Path
		if strings.HasSuffix(p, "/") == false {
			p += "/"
		}

		parts := strings.Split(p, "/")

		if len(parts) == 4 {
			database.list(w, r)
		} else {
			database.get(w, r)
		}
	} else {
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (database *Database) add(w http.ResponseWriter, r *http.Request) {
	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, _ := ShiftPath(r.URL.Path)

	var v interface{}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	doc, ok := v.(map[string]interface{})
	if !ok {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	delete(doc, "id")
	delete(doc, internal.FieldID)
	delete(doc, internal.FieldAccountID)
	delete(doc, internal.FieldOwnerID)

	doc[internal.FieldID] = primitive.NewObjectID()
	doc[internal.FieldAccountID] = auth.AccountID
	doc[internal.FieldOwnerID] = auth.UserID

	ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	if _, err := db.Collection(col).InsertOne(ctx, v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	doc["id"] = doc[internal.FieldID]
	delete(doc, internal.FieldID)

	database.cache.PublishDocument("db-"+col, internal.MsgTypeDBCreated, doc)

	respond(w, http.StatusCreated, doc)
}

type PagedResult struct {
	Page    int64         `json:"page"`
	Size    int64         `json:"size"`
	Total   int64         `json:"total"`
	Results []interface{} `json:"results"`
}

func (database *Database) list(w http.ResponseWriter, r *http.Request) {
	page, size := getPagination(r.URL)

	sortBy := bson.M{"_id": 1}
	if len(r.URL.Query().Get("desc")) > 0 {
		sortBy["_id"] = -1
	}

	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, _ := ShiftPath(r.URL.Path)

	result := PagedResult{
		Page: page,
		Size: size,
	}

	filter := bson.M{}

	// if they're not root
	if !strings.HasPrefix(col, "pub_") && auth.Role < 100 {
		switch internal.ReadPermission(col) {
		case internal.PermGroup:
			filter = bson.M{"accountId": auth.AccountID}
		case internal.PermOwner:
			filter = bson.M{internal.FieldAccountID: auth.AccountID, internal.FieldOwnerID: auth.UserID}
		}
	}

	ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	count, err := db.Collection(col).CountDocuments(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result.Total = count

	skips := size * (page - 1)

	opt := options.Find()
	opt.SetSkip(skips)
	opt.SetLimit(size)
	opt.SetSort(sortBy)

	cur, err := db.Collection(col).Find(ctx, filter, opt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cur.Close(ctx)

	var results []interface{}

	for cur.Next(ctx) {
		var result bson.M
		err := cur.Decode(&result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		result["id"] = result["_id"]
		delete(result, internal.FieldID)
		delete(result, internal.FieldOwnerID)

		results = append(results, result)
	}
	if err := cur.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(results) == 0 {
		results = make([]interface{}, 1)
	}

	result.Results = results

	respond(w, http.StatusOK, result)
}

func (database *Database) get(w http.ResponseWriter, r *http.Request) {
	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	col, id := "", ""

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, r.URL.Path = ShiftPath(r.URL.Path)
	id, r.URL.Path = ShiftPath(r.URL.Path)

	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filter := bson.M{internal.FieldID: oid}

	// if they're not root and repo is not public
	if !strings.HasPrefix(col, "pub_") && auth.Role < 100 {
		switch internal.ReadPermission(col) {
		case internal.PermGroup:
			filter[internal.FieldAccountID] = auth.AccountID
		case internal.PermOwner:
			filter[internal.FieldAccountID] = auth.AccountID
			filter[internal.FieldOwnerID] = auth.UserID
		}
	}

	var result bson.M
	ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	sr := db.Collection(col).FindOne(ctx, filter)
	if err := sr.Decode(&result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if err := sr.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result["id"] = result["_id"]
	delete(result, internal.FieldID)
	delete(result, internal.FieldOwnerID)

	respond(w, http.StatusOK, result)
}

func (database *Database) query(w http.ResponseWriter, r *http.Request) {
	var clauses [][]interface{}
	if err := json.NewDecoder(r.Body).Decode(&clauses); err != nil {
		fmt.Println("error parsing body", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filter := bson.M{}
	for i, clause := range clauses {
		if len(clause) != 3 {
			fmt.Println("clause len not 3 got", len(clause))
			http.Error(w, fmt.Sprintf("The %d query clause did not contains the required 3 parameters (field, operator, value)", i+1), http.StatusBadRequest)
			return
		}

		field, ok := clause[0].(string)
		if !ok {
			fmt.Println("clause[0] not a string", clause[0])
			http.Error(w, fmt.Sprintf("The %d query clause's field parameter must be a string: %v", i+1, clause[0]), http.StatusBadRequest)
			return
		}

		op, ok := clause[1].(string)
		if !ok {
			fmt.Println("clause[1] not a string", clause[1])
			http.Error(w, fmt.Sprintf("The %d query clause's operator must be a string: %v", i+1, clause[1]), http.StatusBadRequest)
			return
		}

		switch op {
		case "=", "==":
			filter[field] = clause[2]
		case "!=", "<>":
			filter[field] = bson.M{"$ne": clause[2]}
		case ">":
			filter[field] = bson.M{"$gt": clause[2]}
		case "<":
			filter[field] = bson.M{"$lt": clause[2]}
		case ">=":
			filter[field] = bson.M{"$gte": clause[2]}
		case "<=":
			filter[field] = bson.M{"$lte": clause[2]}
		case "in":
			filter[field] = bson.M{"$in": clause[2]}
		case "!in", "nin":
			filter[field] = bson.M{"$nin": clause[2]}
		default:
			fmt.Println("unrecognize operation", op)
			http.Error(w, fmt.Sprintf("The %d query clause's operator: %s is not supported at the moment.", i+1, op), http.StatusBadRequest)
			return
		}
	}

	page, size := getPagination(r.URL)

	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		fmt.Println("error extracting conf and auth", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	var col string

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, r.URL.Path = ShiftPath(r.URL.Path)

	payload := PagedResult{
		Page: page,
		Size: size,
	}

	// either not a public repo or not root
	if strings.HasPrefix(col, "pub_") == false && auth.Role < 100 {
		switch internal.ReadPermission(col) {
		case internal.PermGroup:
			filter[internal.FieldAccountID] = auth.AccountID
		case internal.PermOwner:
			filter[internal.FieldAccountID] = auth.AccountID
			filter[internal.FieldOwnerID] = auth.UserID
		}

	}

	ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	count, err := db.Collection(col).CountDocuments(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	payload.Total = count

	if count == 0 {
		payload.Results = make([]interface{}, 0)
		respond(w, http.StatusOK, payload)
		return
	}

	skips := size * (page - 1)

	sort := r.URL.Query().Get("sort")
	if len(sort) == 0 {
		sort = "_id"
	}

	sortBy := bson.M{sort: -1}

	opt := options.Find()
	opt.SetSkip(skips)
	opt.SetLimit(size)
	opt.SetSort(sortBy)

	cur, err := db.Collection(col).Find(ctx, filter, opt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cur.Close(ctx)

	var results []interface{}

	for cur.Next(ctx) {
		var result bson.M
		if err := cur.Decode(&result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		result["id"] = result["_id"]
		delete(result, internal.FieldID)
		delete(result, internal.FieldOwnerID)

		results = append(results, result)
	}

	if err := cur.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(results) == 0 {
		results = make([]interface{}, 1)
	}

	payload.Results = results

	respond(w, http.StatusOK, payload)
}

func (database *Database) update(w http.ResponseWriter, r *http.Request) {
	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	col, id := "", ""

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, r.URL.Path = ShiftPath(r.URL.Path)
	id, r.URL.Path = ShiftPath(r.URL.Path)

	var v interface{}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	doc, ok := v.(map[string]interface{})
	if !ok {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	delete(doc, "id")
	delete(doc, internal.FieldID)
	delete(doc, internal.FieldAccountID)
	delete(doc, internal.FieldOwnerID)

	filter := bson.M{internal.FieldID: oid}

	// if they are not "root", we use permission
	if auth.Role < 100 {
		switch internal.WritePermission(col) {
		case internal.PermGroup:
			filter[internal.FieldAccountID] = auth.AccountID
		case internal.PermOwner:
			filter[internal.FieldAccountID] = auth.AccountID
			filter[internal.FieldOwnerID] = auth.UserID
		}
	}

	newProps := bson.M{}
	for k, v := range doc {
		newProps[k] = v
	}

	update := bson.M{"$set": newProps}

	ctx := context.Background()
	res := db.Collection(col).FindOneAndUpdate(ctx, filter, update)
	if err := res.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var result bson.M
	sr := db.Collection(col).FindOne(context.Background(), filter)
	if err := sr.Decode(&result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if err := sr.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result["id"] = result["_id"]
	delete(result, internal.FieldID)

	database.cache.PublishDocument("db-"+col, internal.MsgTypeDBUpdated, result)

	respond(w, http.StatusOK, result)
}

func (database *Database) del(w http.ResponseWriter, r *http.Request) {
	conf, auth, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	col, id := "", ""

	_, r.URL.Path = ShiftPath(r.URL.Path)
	col, r.URL.Path = ShiftPath(r.URL.Path)
	id, r.URL.Path = ShiftPath(r.URL.Path)

	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filter := bson.M{internal.FieldID: oid}

	// if they're not root
	if auth.Role < 100 {
		switch internal.WritePermission(col) {
		case internal.PermGroup:
			filter[internal.FieldAccountID] = auth.AccountID
		case internal.PermOwner:
			filter[internal.FieldAccountID] = auth.AccountID
			filter[internal.FieldOwnerID] = auth.UserID

		}
	}
	ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	res, err := db.Collection(col).DeleteOne(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	database.cache.PublishDocument("db-"+col, internal.MsgTypeDBDeleted, id)
	respond(w, http.StatusOK, res.DeletedCount)
}

func (database *Database) newID(w http.ResponseWriter, r *http.Request) {
	id := primitive.NewObjectID()
	respond(w, http.StatusOK, id.Hex())
}

func (database *Database) listCollections(w http.ResponseWriter, r *http.Request) {
	conf, _, err := middleware.Extract(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	db := database.client.Database(conf.Name)

	ctx := context.Background()

	cur, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cur.Close(ctx)

	var names []string
	for cur.Next(ctx) {
		var result bson.M
		err := cur.Decode(&result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		names = append(names, fmt.Sprintf("%v", result["name"]))
	}

	respond(w, http.StatusOK, names)
}

func getPagination(u *url.URL) (page int64, size int64) {
	var err error

	page, err = strconv.ParseInt(u.Query().Get("page"), 10, 64)
	if err != nil {
		page = 1
	}

	size, err = strconv.ParseInt(u.Query().Get("size"), 10, 64)
	if err != nil {
		size = 25
	}

	return
}
