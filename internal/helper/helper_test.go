package helper

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/stretchr/testify/require"

	"twocan/internal/identity"
	"twocan/internal/logbox"
	pb "twocan/internal/proto"
)

var loopback = []string{"/ip4/127.0.0.1/tcp/0"}

func TestResourcesLimits(t *testing.T) {
	r := Resources()
	require.NotNil(t, r.Limit)
	require.Equal(t, 30*time.Minute, r.Limit.Duration)
	require.Equal(t, int64(4<<20), r.Limit.Data)
	require.Equal(t, relay.DefaultResources().MaxCircuits, r.MaxCircuits)
}

func TestStartStableIdentity(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "helper.key")
	ids := make([]peer.ID, 2)
	for i := range ids {
		key, err := identity.LoadOrCreateKey(keyPath)
		require.NoError(t, err)
		h, d, err := Start(context.Background(), key, loopback)
		require.NoError(t, err)
		ids[i] = h.ID()
		require.NoError(t, d.Close())
		require.NoError(t, h.Close())
	}
	require.Equal(t, ids[0], ids[1])
}

func TestHelperSpeaksTwocanDHT(t *testing.T) {
	key, err := identity.LoadOrCreateKey(filepath.Join(t.TempDir(), "helper.key"))
	require.NoError(t, err)
	h, d, err := Start(context.Background(), key, loopback)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close(); _ = h.Close() })

	c, err := libp2p.New(libp2p.ListenAddrStrings(loopback...))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx, peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}))
	require.Eventually(t, func() bool {
		ps, err := c.Peerstore().GetProtocols(h.ID())
		if err != nil {
			return false
		}
		for _, p := range ps {
			if p == "/twocan/kad/1.0.0" {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
}

func startTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings(loopback...))
	require.NoError(t, err)
	t.Cleanup(func() { h.Close() })
	return h
}

func hasProtocol(h host.Host, id protocol.ID) bool {
	return slices.Contains(h.Mux().Protocols(), id)
}

// Without readers the helper offers no logs protocol, but still deletes
// old reports, so turning the feature off leaves none behind.
func TestStartLogsWithoutReadersOnlyPrunes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	st, err := logbox.OpenStore(dir, logbox.DefaultLimits())
	require.NoError(t, err)
	u, err := st.Begin()
	require.NoError(t, err)
	_, err = u.Commit(&pb.ReportInfo{}, time.Now().Add(-8*24*time.Hour))
	require.NoError(t, err)

	h := startTestHost(t)
	stop, err := StartLogs(t.Context(), h, dir, nil, logbox.DefaultLimits(), nil)
	require.NoError(t, err)
	defer stop()
	rs, err := st.List()
	require.NoError(t, err)
	require.Empty(t, rs, "the 8-day-old report was deleted")
	require.False(t, hasProtocol(h, logbox.UploadProtocol))
	require.False(t, hasProtocol(h, logbox.ReadProtocol))
}

func TestStartLogsWithReaders(t *testing.T) {
	h := startTestHost(t)
	stop, err := StartLogs(t.Context(), h, filepath.Join(t.TempDir(), "logs"), []peer.ID{h.ID()}, logbox.DefaultLimits(), nil)
	require.NoError(t, err)
	defer stop()
	require.True(t, hasProtocol(h, logbox.UploadProtocol))
	require.True(t, hasProtocol(h, logbox.ReadProtocol))
}

func TestStartLogsNothingWithoutDirOrReaders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	stop, err := StartLogs(t.Context(), startTestHost(t), dir, nil, logbox.DefaultLimits(), nil)
	require.NoError(t, err)
	stop()
	require.NoDirExists(t, dir)
}
