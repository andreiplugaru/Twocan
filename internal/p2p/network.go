package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	msmux "github.com/multiformats/go-multistream"
)

// probeTimeout is how long an older connection has to answer once the same
// peer opens a new one.
const probeTimeout = 3 * time.Second

// probe pings over connection c alone and returns an error if it does not
// answer within timeout. (Pinging the peer would use its preferred
// connection: a dead direct one, even when a relayed one works.)
func probe(ctx context.Context, c network.Conn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := c.NewStream(ctx)
	if err != nil {
		return err
	}
	defer s.Reset()
	if err := s.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := msmux.SelectProtoOrFail(ping.ID, io.ReadWriteCloser(s)); err != nil {
		return err
	}
	if err := s.SetProtocol(ping.ID); err != nil {
		return err
	}
	out := make([]byte, ping.PingSize)
	if _, err := rand.Read(out); err != nil {
		return err
	}
	if _, err := s.Write(out); err != nil {
		return err
	}
	in := make([]byte, ping.PingSize)
	if _, err := io.ReadFull(s, in); err != nil {
		return err
	}
	if !bytes.Equal(in, out) {
		return errors.New("ping answered with other bytes")
	}
	return nil
}

// closeDead probes conns at once and closes those that do not answer
// within timeout, returning how many it closed.
func (n *Node) closeDead(ctx context.Context, conns []network.Conn, timeout time.Duration, why string) int {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		closed int
	)
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := probe(ctx, c, timeout)
			if err == nil || ctx.Err() != nil {
				return
			}
			n.log.Info("closing a connection that stopped answering", "peer", c.RemotePeer(),
				"remote", c.RemoteMultiaddr(), "why", why, "err", err)
			_ = c.Close()
			mu.Lock()
			closed++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return closed
}

// CloseUnresponsive probes every direct connection and closes those that
// do not answer within timeout. After a network change, connections over
// the network we left stay open until they time out (about 30 s) while
// nothing gets through; closing them lets the peer be reached again at
// once. It returns the peers left with no connection.
func (n *Node) CloseUnresponsive(ctx context.Context, timeout time.Duration) []peer.ID {
	var direct []network.Conn
	for _, c := range n.host.Network().Conns() {
		if !isCircuit(c.RemoteMultiaddr()) {
			direct = append(direct, c)
		}
	}
	n.closeDead(ctx, direct, timeout, "network changed")
	seen := map[peer.ID]bool{}
	var gone []peer.ID
	for _, c := range direct {
		if id := c.RemotePeer(); !seen[id] {
			seen[id] = true
			if len(n.host.Network().ConnsToPeer(id)) == 0 {
				gone = append(gone, id)
			}
		}
	}
	return gone
}

// probeOlder runs when conn to a peer opens: if older connections to that
// peer exist, they are probed and closed if dead. A peer that connects again
// over another path has often changed networks, and libp2p sends new streams
// over the connection it prefers, a dead direct one included, until that
// times out.
func (n *Node) probeOlder(ctx context.Context, conn network.Conn) {
	var older []network.Conn
	for _, c := range n.host.Network().ConnsToPeer(conn.RemotePeer()) {
		if c != conn && c.Stat().Opened.Before(conn.Stat().Opened) {
			older = append(older, c)
		}
	}
	if len(older) > 0 {
		n.closeDead(ctx, older, probeTimeout, "the peer connected again")
	}
}

// ConnectHelper connects to helper h, forgetting earlier failed dials to it
// (a helper that is back must not wait out the dial backoff).
func (n *Node) ConnectHelper(ctx context.Context, h peer.AddrInfo) error {
	n.clearBackoff(h.ID)
	return n.connect(ctx, h)
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

// newConnWatcher calls probeOlder, in the background, for each new
// connection; the probes end with ctx.
func (n *Node) newConnWatcher(ctx context.Context) network.Notifiee {
	return &network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			if ctx.Err() == nil {
				go n.probeOlder(ctx, c)
			}
		},
	}
}
