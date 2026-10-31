package core

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/helper"
	"twocan/internal/logbox"
	"twocan/internal/logging"
)

func TestSendLogToHelper(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: real libp2p hosts on loopback")
	}
	reader, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	t.Cleanup(func() { reader.Close() })

	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	h, d, err := helper.Start(t.Context(), key, []string{"/ip4/127.0.0.1/tcp/0"})
	require.NoError(t, err)
	t.Cleanup(func() { d.Close(); h.Close() })
	stop, err := helper.StartLogs(t.Context(), h, t.TempDir()+"/logs", []peer.ID{reader.ID()}, logbox.DefaultLimits(), nil)
	require.NoError(t, err)
	t.Cleanup(stop)
	boot := h.Addrs()[0].String() + "/p2p/" + h.ID().String()

	dir := t.TempDir()
	lg, err := logging.Open(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { lg.Close() })
	n, err := Start(t.Context(), Options{
		DataDir: dir, Bootstrap: []string{boot}, ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Log: lg.Logger, App: "cli", Version: "v9.9.9",
	})
	require.NoError(t, err)
	t.Cleanup(func() { n.Stop() })
	require.NoError(t, n.SetDisplayName("Ann"))

	code, err := n.SendLog(t.Context(), "it broke")
	require.NoError(t, err)

	require.NoError(t, reader.Connect(t.Context(), peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}))
	var gz bytes.Buffer
	info, err := logbox.Get(t.Context(), reader, h.ID(), code, &gz)
	require.NoError(t, err)
	require.Equal(t, n.PeerID().String(), info.PeerId)
	require.Equal(t, "cli", info.Details.GetApp())
	require.Equal(t, "v9.9.9", info.Details.GetVersion())
	require.Equal(t, "Ann", info.Details.GetName())
	require.Equal(t, "it broke", info.Details.GetNote())
	zr, err := gzip.NewReader(&gz)
	require.NoError(t, err)
	text, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Contains(t, string(text), "msg=starting")
}

func TestSendLogWithoutHelper(t *testing.T) {
	n, err := Start(t.Context(), Options{DataDir: t.TempDir(), Bootstrap: []string{}, ListenAddrs: []string{}})
	require.NoError(t, err)
	t.Cleanup(func() { n.Stop() })
	start := time.Now()
	_, err = n.SendLog(t.Context(), "")
	require.ErrorIs(t, err, ErrNoHelper)
	require.Less(t, time.Since(start), time.Second)
}
