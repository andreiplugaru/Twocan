package p2p

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/logtest"
)

// freezer is a TCP proxy that can stop passing data while keeping its
// connections open, as a connection over a network the phone just left
// does.
type freezer struct {
	l      net.Listener
	frozen atomic.Bool
}

func newFreezer(t *testing.T, to string) *freezer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	f := &freezer{l: l}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", to)
			if err != nil {
				c.Close()
				continue
			}
			go f.pipe(up, c)
			go f.pipe(c, up)
		}
	}()
	return f
}

func (f *freezer) pipe(dst, src net.Conn) {
	defer dst.Close()
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 && !f.frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				src.Close()
			}
			return
		}
	}
}

func (f *freezer) addr() ma.Multiaddr {
	return ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", f.l.Addr().(*net.TCPAddr).Port))
}

func tcpHostPort(t *testing.T, n *Node) string {
	t.Helper()
	for _, a := range n.Host().Addrs() {
		if port, err := a.ValueForProtocol(ma.P_TCP); err == nil {
			return "127.0.0.1:" + port
		}
	}
	t.Fatal("no TCP address")
	return ""
}

// CloseUnresponsive closes the connections that stopped answering and
// keeps the ones that still work.
func TestCloseUnresponsive(t *testing.T) {
	a, b, c := newTestNode(t), newTestNode(t), newTestNode(t)
	f := newFreezer(t, tcpHostPort(t, b))
	require.NoError(t, a.Host().Connect(t.Context(), peer.AddrInfo{ID: b.Host().ID(), Addrs: []ma.Multiaddr{f.addr()}}))
	require.NoError(t, a.Host().Connect(t.Context(), peer.AddrInfo{ID: c.Host().ID(), Addrs: c.Host().Addrs()}))

	f.frozen.Store(true)
	closed := a.CloseUnresponsive(t.Context(), 300*time.Millisecond)
	require.Equal(t, []peer.ID{b.Host().ID()}, closed)
	connected, _ := a.ConnInfo(b.Host().ID())
	require.False(t, connected, "the frozen connection is closed")
	connected, _ = a.ConnInfo(c.Host().ID())
	require.True(t, connected, "the working connection is kept")
}

// ReconnectHelpers connects to a helper at once, though dialling it failed
// moments ago.
func TestReconnectHelpers(t *testing.T) {
	key := newTestKey(t)
	helperID, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", l.Addr().(*net.TCPAddr).Port)
	require.NoError(t, l.Close())
	boot, err := ParseBootstrap([]string{addr + "/p2p/" + helperID.String()})
	require.NoError(t, err)
	log, logs := logtest.New()
	a := newTestNodeCfg(t, Config{PrivKey: newTestKey(t), ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"}, Bootstrap: boot, Log: log})
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), `msg="bootstrap peer unreachable"`)
	}, 5*time.Second, 10*time.Millisecond, "the first dial failed")

	newTestNodeCfg(t, Config{PrivKey: key, ListenAddrs: []string{addr}})
	a.ReconnectHelpers(t.Context())
	require.Equal(t, network.Connected, a.Host().Network().Connectedness(helperID))
}
