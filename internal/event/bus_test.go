package event

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func recv(t *testing.T, ch <-chan any) any {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func TestBusFanOut(t *testing.T) {
	b := NewBus()
	c1, cancel1 := b.Subscribe()
	defer cancel1()
	c2, cancel2 := b.Subscribe()
	defer cancel2()

	events := []any{ContactChanged{}, Connectivity{Connected: true}, Reachability{}}
	for _, e := range events {
		b.Publish(e)
	}
	for _, e := range events {
		require.Equal(t, e, recv(t, c1))
		require.Equal(t, e, recv(t, c2))
	}
}

func TestBusPublishNeverBlocks(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(MessageDelivered{ID: []byte{1}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked on an unread subscriber")
	}
}

func TestBusCancel(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	cancel()
	_, ok := <-ch
	require.False(t, ok, "channel must be closed after cancel")
	require.NotPanics(t, func() {
		cancel()
		b.Publish(ContactChanged{})
	})
}
