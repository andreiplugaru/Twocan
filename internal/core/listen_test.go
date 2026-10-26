package core

import (
	"context"
	"net"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/store"
)

func TestWithSavedPorts(t *testing.T) {
	listen := []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
		"/ip6/::/tcp/0",           // nothing saved for it
		"/ip4/127.0.0.1/tcp/4001", // fixed port: left alone
	}
	saved := []ma.Multiaddr{
		ma.StringCast("/ip4/0.0.0.0/tcp/38189"),
		ma.StringCast("/ip4/0.0.0.0/udp/38518/quic-v1"),
		ma.StringCast("/ip4/127.0.0.1/tcp/9999"), // not port 0 in listen
	}
	require.Equal(t, []string{
		"/ip4/0.0.0.0/tcp/38189",
		"/ip4/0.0.0.0/udp/38518/quic-v1",
		"/ip6/::/tcp/0",
		"/ip4/127.0.0.1/tcp/4001",
	}, withSavedPorts(listen, saved))
}

func TestPortless(t *testing.T) {
	require.Equal(t, "/ip4/0.0.0.0/udp/0/quic-v1", portless(ma.StringCast("/ip4/0.0.0.0/udp/38518/quic-v1")))
	require.Equal(t, "/ip6/::/tcp/0", portless(ma.StringCast("/ip6/::/tcp/5")))
}

// startListening starts a node in dir listening on loopback TCP and QUIC,
// random ports.
func startListening(t *testing.T, dir string) *Node {
	t.Helper()
	n, err := Start(t.Context(), Options{
		DataDir:     dir,
		Bootstrap:   []string{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0", "/ip4/127.0.0.1/udp/0/quic-v1"},
	})
	require.NoError(t, err)
	return n
}

func listenStrings(n *Node) []string {
	var out []string
	for _, a := range n.h.Network().ListenAddresses() {
		if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
			continue
		}
		out = append(out, a.String())
	}
	return out
}

func TestStartKeepsPorts(t *testing.T) {
	dir := t.TempDir()
	n := startListening(t, dir)
	first := listenStrings(n)
	require.Len(t, first, 2)
	require.NoError(t, n.Stop())

	n = startListening(t, dir)
	defer func() { require.NoError(t, n.Stop()) }()
	require.ElementsMatch(t, first, listenStrings(n))
}

func TestStartReplacesTakenPort(t *testing.T) {
	dir := t.TempDir()
	n := startListening(t, dir)
	first := listenStrings(n)
	require.NoError(t, n.Stop())

	// Someone else takes our TCP port while we are stopped.
	var tcpAddr, quicAddr string
	for _, a := range first {
		if _, err := ma.StringCast(a).ValueForProtocol(ma.P_TCP); err == nil {
			tcpAddr = a
		} else {
			quicAddr = a
		}
	}
	port, err := ma.StringCast(tcpAddr).ValueForProtocol(ma.P_TCP)
	require.NoError(t, err)
	l, err := net.Listen("tcp4", "127.0.0.1:"+port)
	require.NoError(t, err)
	defer l.Close()

	n = startListening(t, dir)
	got := listenStrings(n)
	require.Len(t, got, 2, "TCP listens on a new port")
	require.Contains(t, got, quicAddr, "QUIC keeps its port")
	require.NotContains(t, got, tcpAddr)
	require.NoError(t, n.Stop())

	// The new port is the one kept from now on.
	n = startListening(t, dir)
	defer func() { require.NoError(t, n.Stop()) }()
	require.ElementsMatch(t, got, listenStrings(n))
}

func TestStartAllSavedPortsTaken(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/" + dbFile)
	require.NoError(t, err)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	taken := "/ip4/127.0.0.1/tcp/" + portOf(t, l.Addr())
	require.NoError(t, st.SetSetting(context.Background(), store.SettingListenAddrs, taken))
	require.NoError(t, st.Close())

	n, err := Start(t.Context(), Options{
		DataDir:     dir,
		Bootstrap:   []string{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	require.NoError(t, err, "falls back to a random port")
	defer func() { require.NoError(t, n.Stop()) }()
	got := listenStrings(n)
	require.Len(t, got, 1)
	require.NotEqual(t, taken, got[0])
}

func portOf(t *testing.T, a net.Addr) string {
	t.Helper()
	_, port, err := net.SplitHostPort(a.String())
	require.NoError(t, err)
	return port
}
