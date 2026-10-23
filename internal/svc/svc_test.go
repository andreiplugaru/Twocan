package svc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/stretchr/testify/require"
)

const waitFor = 2 * time.Second

func TestTrackerCloseIdempotentAndCancels(t *testing.T) {
	tr := NewTracker()
	tr.Close()
	tr.Close()
	require.ErrorIs(t, tr.Context().Err(), context.Canceled)
}

func TestTrackerRefusesWorkAfterClose(t *testing.T) {
	tr := NewTracker()
	tr.Close()
	require.False(t, tr.Track())
	ran := make(chan struct{}, 1)
	require.False(t, tr.Go(func(context.Context) { ran <- struct{}{} }))
	select {
	case <-ran:
		t.Fatal("Go ran fn after Close")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTrackerCloseWaitsForRunningWork(t *testing.T) {
	tr := NewTracker()
	started := make(chan struct{})
	var finished atomic.Bool
	require.True(t, tr.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done() // runs until Close cancels it
		time.Sleep(20 * time.Millisecond)
		finished.Store(true)
	}))

	require.True(t, tr.Track()) // a handler-style unit of work
	var handlerDone atomic.Bool
	go func() {
		defer tr.Done()
		<-tr.Context().Done()
		time.Sleep(20 * time.Millisecond)
		handlerDone.Store(true)
	}()

	<-started
	tr.Close()
	require.True(t, finished.Load())
	require.True(t, handlerDone.Load())
}

func TestLocksSerializePerKey(t *testing.T) {
	var l Locks[string]
	var inside atomic.Int32
	var overlapped atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := l.Lock("a")
			defer unlock()
			if inside.Add(1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(2 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()
	require.False(t, overlapped.Load())
}

func TestLocksIndependentKeys(t *testing.T) {
	var l Locks[string]
	unlockA := l.Lock("a")
	defer unlockA()
	done := make(chan struct{})
	go func() {
		l.Lock("b")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("lock on b blocked by a")
	}
}

// fakeStream records SetDeadline and Reset; every other method is unused.
type fakeStream struct {
	network.Stream
	mu       sync.Mutex
	deadline time.Time
	reset    chan struct{}
}

func newFakeStream() *fakeStream { return &fakeStream{reset: make(chan struct{})} }

func (s *fakeStream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	return nil
}

func (s *fakeStream) Reset() error {
	close(s.reset)
	return nil
}

func (s *fakeStream) getDeadline() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deadline
}

func (s *fakeStream) wasReset() bool {
	select {
	case <-s.reset:
		return true
	default:
		return false
	}
}

func TestTimeoutResetsStreamWhenExpired(t *testing.T) {
	str := newFakeStream()
	before := time.Now()
	_, cancel := Timeout(context.Background(), str, 30*time.Millisecond)
	defer cancel()
	require.WithinDuration(t, before.Add(30*time.Millisecond), str.getDeadline(), 20*time.Millisecond)
	select {
	case <-str.reset:
	case <-time.After(waitFor):
		t.Fatal("stream not reset after timeout")
	}
}

func TestTimeoutResetsStreamWhenParentCancelled(t *testing.T) {
	str := newFakeStream()
	parent, cancelParent := context.WithCancel(context.Background())
	_, cancel := Timeout(parent, str, time.Hour)
	defer cancel()
	cancelParent()
	select {
	case <-str.reset:
	case <-time.After(waitFor):
		t.Fatal("stream not reset when parent cancelled")
	}
}

func TestTimeoutCancelDoesNotReset(t *testing.T) {
	str := newFakeStream()
	ctx, cancel := Timeout(context.Background(), str, time.Hour)
	cancel()
	require.Error(t, ctx.Err())
	time.Sleep(20 * time.Millisecond)
	require.False(t, str.wasReset())
}

func TestGuardWithoutDeadline(t *testing.T) {
	str := newFakeStream()
	ctx, cancelCtx := context.WithCancel(context.Background())
	stop := Guard(ctx, str)
	defer stop()
	require.True(t, str.getDeadline().IsZero())
	cancelCtx()
	select {
	case <-str.reset:
	case <-time.After(waitFor):
		t.Fatal("stream not reset when ctx cancelled")
	}

	str2 := newFakeStream()
	ctx2, cancel2 := context.WithCancel(context.Background())
	Guard(ctx2, str2)()
	cancel2()
	time.Sleep(20 * time.Millisecond)
	require.False(t, str2.wasReset())
}
