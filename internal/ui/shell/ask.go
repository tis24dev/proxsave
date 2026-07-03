package shell

import (
	"context"
	"sync"
)

// Ask pushes scr onto the Session's screen stack and blocks until the screen
// resolves, ctx is cancelled, or the program terminates. It is the only
// bridge between engine goroutines and the UI event loop.
//
// Concurrency contract: the resolve callback is once-guarded and delivers
// into a buffered channel, so the tea loop never blocks and a user answer
// racing a cancellation cannot double-send. Every Ask also selects on the
// session's done channel, so a dead program can never strand the engine.
//
// Engine flows are expected to issue one Ask at a time per Session
// (sequential prompts); concurrent Asks render stacked, with keys going to
// the most recent screen.
func Ask[T any](ctx context.Context, s *Session, scr AskScreen[T]) (T, error) {
	var zero T
	type result struct {
		v   T
		err error
	}
	resp := make(chan result, 1)
	var once sync.Once
	resolve := func(v T, err error) {
		once.Do(func() { resp <- result{v: v, err: err} })
	}
	scr.Bind(resolve)
	id := s.nextID.Add(1)
	if si, ok := any(scr).(screenIdentity); ok {
		si.setID(id)
	}
	s.prog.Send(pushScreenMsg{
		id:     id,
		screen: scr,
		abort:  func() { resolve(zero, ErrAborted) },
	})
	select {
	case r := <-resp:
		// The screen normally pops itself via Resolve's command; this
		// removal is a by-id idempotent no-op then, and a safety net for
		// screens that resolve without returning the command.
		s.prog.Send(removeScreenMsg{id: id})
		return r.v, r.err
	case <-ctx.Done():
		// Best-effort removal; Send is FIFO so the removal lands before
		// any screen the engine pushes next.
		s.prog.Send(removeScreenMsg{id: id})
		return zero, ctx.Err()
	case <-s.done:
		return zero, s.closedErr()
	}
}
