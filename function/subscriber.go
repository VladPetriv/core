package function

import (
	"fmt"
	"sync"
	"time"

	"github.com/staticbackendhq/core/cache"
	"github.com/staticbackendhq/core/logger"
	"github.com/staticbackendhq/core/model"
)

type Subscriber struct {
	PubSub            cache.Volatilizer
	GetExecEnv        func(msg model.Command) (*ExecutionEnvironment, error)
	Log               *logger.Logger
	IsPrimaryInstance bool

	relax sync.Map
}

// Start starts the system event subscription.
// This channel is responsible of executing functions that match the
// topic/trigger
func (sub *Subscriber) Start() {
	receiver := make(chan model.Command)
	close := make(chan bool)

	go sub.PubSub.Subscribe(receiver, "", "sbsys", close)

	for {
		select {
		case msg := <-receiver:
			// only handle function execution on the primary instance
			// otherwise it would cause duplication work.
			if sub.IsPrimaryInstance {
				go sub.process(msg)
			}
		case <-close:
			sub.Log.Info().Msg("system event channel closed?!?")
		}
	}
}

func (sub *Subscriber) process(msg model.Command) {
	switch msg.Type {
	case model.MsgTypeChanOut,
		model.MsgTypeDBCreated,
		model.MsgTypeDBUpdated,
		model.MsgTypeDBDeleted:
		sub.handleRealtimeEvents(msg)
	default:
		// for user triggered events, we enforce a max of 5 msg / 60 secs
		v, ok := sub.relax.Load(msg.Auth.UserID)
		if !ok {
			v = int64(0)

			go func(key string) {
				time.Sleep(60 * time.Second)
				sub.relax.Delete(key)
			}(msg.Auth.UserID)
		}

		n, ok := v.(int64)
		if !ok {
			sub.Log.Warn().Msgf("subscriber.process(): unable to cast %v into int64", v)
			return
		} else if n >= 5 {
			sub.Log.Warn().Msgf("user exeeded amount of allowed message in 60 second: %d", n)
			//TODO: This silently returns, should this app owner get some
			// notification about their users flooding the system?
			return
		}

		n += 1
		sub.relax.Store(msg.Auth.UserID, n)

		sub.handleRealtimeEvents(msg)
	}
}

func (sub *Subscriber) handleRealtimeEvents(msg model.Command) {
	exe, err := sub.GetExecEnv(msg)
	if err != nil {
		sub.Log.Error().Err(err).Msgf("cannot retrieve base from token: %s", msg.Token)
		return
	}

	var ids []string

	key := fmt.Sprintf("%s:%s", exe.BaseName, msg.Channel)
	if err := sub.PubSub.GetTyped(key, &ids); err != nil {
		funcs, err := exe.DataStore.ListFunctionsByTrigger(exe.BaseName, msg.Channel)
		if err != nil {
			sub.Log.Error().Err(err).Msg("error getting functions by trigger")
			return
		}

		for _, fn := range funcs {
			if err := sub.PubSub.SetTyped("fn_"+fn.ID, fn); err != nil {
				sub.Log.Error().Err(err).Msg("error adding function  to cache")
				return
			}

			ids = append(ids, fn.ID)
		}

		if err := sub.PubSub.SetTyped(key, ids); err != nil {
			sub.Log.Error().Err(err).Msg("unable to publish message")
		}
	}

	for _, id := range ids {
		var fn model.ExecData
		if err := sub.PubSub.GetTyped("fn_"+id, &fn); err != nil {
			sub.Log.Error().Err(err).Msg("error getting function out of cache")
			return
		}

		exe.Data = fn
		go func(ex *ExecutionEnvironment) {
			if err := ex.Execute(msg); err != nil {
				sub.Log.Error().Err(err).Msgf(`executing "%s" function failed"`, ex.Data.FunctionName)
			}
		}(exe)
	}
}
