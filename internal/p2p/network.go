package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
)

// CloseUnresponsive pings every peer we have a direct connection to and
// closes the connections to those that do not answer within timeout. After
// a network change, connections over the network we left stay open until
// they time out (about 30 s) while nothing gets through; closing them lets
// the peer be reached again at once. It returns the peers it disconnected.
func (n *Node) CloseUnresponsive(ctx context.Context, timeout time.Duration) []peer.ID {
	seen := map[peer.ID]bool{}
	var ids []peer.ID
	for _, c := range n.host.Network().Conns() {
		if id := c.RemotePeer(); !seen[id] && !isCircuit(c.RemoteMultiaddr()) {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	var (
		mu     sync.Mutex
		closed []peer.ID
		wg     sync.WaitGroup
	)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			// Ping closes the channel without a result when pctx ends.
			res, ok := <-ping.Ping(pctx, n.host, id)
			if ok && res.Error == nil || ctx.Err() != nil {
				return
			}
			err := res.Error
			if !ok {
				err = pctx.Err()
			}
			n.log.Info("closing connections that stopped answering", "peer", id, "err", err)
			_ = n.host.Network().ClosePeer(id)
			mu.Lock()
			closed = append(closed, id)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return closed
}

// ReconnectHelpers connects to every helper we are not connected to, at
// once, forgetting earlier failed dials: after a network change the relay
// and the DHT need the helper.
func (n *Node) ReconnectHelpers(ctx context.Context) {
	for _, b := range n.boot {
		n.clearBackoff(b.ID)
	}
	n.bootstrap(ctx, n.boot)
}
