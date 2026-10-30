package core

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/p2p"
)

const (
	// defaultSettle is how long a network change is left to settle before
	// it is acted on: one change brings a burst of notifications.
	defaultSettle = time.Second
	// probeTimeout is how long a connection has to answer a ping after a
	// network change before it is closed.
	probeTimeout = 3 * time.Second
)

// netWatcher is what reacting to a network change needs from the libp2p
// node; *p2p.Node implements it.
type netWatcher interface {
	CloseUnresponsive(ctx context.Context, timeout time.Duration) []peer.ID
	ReconnectHelpers(ctx context.Context)
}

var _ netWatcher = (*p2p.Node)(nil)

// NetworkChanged tells the node that the device's networks changed (the
// Android app calls it when a network comes or goes, or its addresses
// change). Shortly after, connections that stopped answering are closed,
// so their contacts are reconnected, the helper is reconnected and
// contacts we are not connected to are reached. It does not block.
func (n *Node) NetworkChanged() {
	select {
	case n.netChange <- struct{}{}:
	default: // a change is already waiting
	}
}

// watchNetwork acts on NetworkChanged, once per burst of calls.
func (n *Node) watchNetwork(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.netChange:
		}
		settled := time.NewTimer(n.iv.settle)
	settle:
		for {
			select {
			case <-ctx.Done():
				settled.Stop()
				return
			case <-n.netChange:
				settled.Reset(n.iv.settle)
			case <-settled.C:
				break settle
			}
		}
		n.onNetworkChanged(ctx)
	}
}

// onNetworkChanged is watchNetwork's work.
func (n *Node) onNetworkChanged(ctx context.Context) {
	n.log.Info("network changed: checking connections")
	if n.net != nil {
		// A closed contact connection is reconnected by onConnectedness.
		closed := n.net.CloseUnresponsive(ctx, probeTimeout)
		n.log.Info("closed connections that stopped answering", "peers", len(closed))
		n.net.ReconnectHelpers(ctx)
	}
	n.sweepPresence(ctx)
}
