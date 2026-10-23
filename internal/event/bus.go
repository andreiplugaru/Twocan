// Package event is a small in-process publish/subscribe bus that decouples
// the network layer from the UI.
package event

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/store"
)

// subscriberBuffer is the per-subscriber channel capacity.
const subscriberBuffer = 256

// MessageReceived is published when a message arrives from a peer.
type MessageReceived struct{ Msg store.Message }

// MessageDelivered is published when a peer acknowledges a sent message.
type MessageDelivered struct {
	Peer peer.ID
	ID   []byte
}

// ContactChanged is published when a contact is added or its state changes.
type ContactChanged struct{ Peer peer.ID }

// Connectivity is published when the connection state to a peer changes.
type Connectivity struct {
	Peer      peer.ID
	Connected bool
	Relayed   bool
}

// Reachability is published when our own NAT reachability changes.
type Reachability struct{ Status network.Reachability }

// Bus fans events out to subscribers. Publish never blocks.
type Bus struct {
	mu   sync.Mutex
	subs map[chan any]struct{}
}

// NewBus returns an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[chan any]struct{})}
}

// Publish delivers e to every subscriber. A subscriber whose buffer is full
// misses the event. Event contents are never logged.
func (b *Bus) Publish(e any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of events and a cancel function that
// unsubscribes and closes the channel. cancel is idempotent.
func (b *Bus) Subscribe() (<-chan any, func()) {
	ch := make(chan any, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			close(ch)
			b.mu.Unlock()
		})
	}
}
