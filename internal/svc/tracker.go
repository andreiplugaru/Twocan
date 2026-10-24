// Package svc holds the plumbing shared by Twocan's stream services
// (contacts, chat): a lifetime tracker for background work, a per-key lock
// map and a stream timeout guard.
package svc

import (
	"context"
	"sync"
)

// Tracker ties background work (async flushes, inbound stream handlers) to a
// service's lifetime. Close cancels the lifetime context, refuses new work and
// waits for running work to finish.
type Tracker struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex // guards closed and wg.Add against Close
	closed bool
	wg     sync.WaitGroup
}

// NewTracker returns an open Tracker.
func NewTracker() *Tracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Tracker{ctx: ctx, cancel: cancel}
}

// Context is the service lifetime context; it is cancelled by Close.
func (t *Tracker) Context() context.Context { return t.ctx }

// Track registers one unit of work, to be ended with Done. It returns false,
// registering nothing, once Close has been called.
func (t *Tracker) Track() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.wg.Add(1)
	return true
}

// Done ends a unit of work registered by a successful Track.
func (t *Tracker) Done() { t.wg.Done() }

// Go runs fn in a tracked goroutine with the lifetime context. It returns
// false, without running fn, once Close has been called.
func (t *Tracker) Go(fn func(ctx context.Context)) bool {
	if !t.Track() {
		return false
	}
	go func() {
		defer t.Done()
		fn(t.ctx)
	}()
	return true
}

// Wait blocks until all tracked work has finished, without closing. Work
// must not be added concurrently with Wait (tests use it once traffic stops).
func (t *Tracker) Wait() { t.wg.Wait() }

// Close refuses new work, cancels the lifetime context and waits for running
// work to finish. It is safe to call more than once.
func (t *Tracker) Close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.cancel()
	t.wg.Wait()
}
