package p2p

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitAddrs(t *testing.T) {
	require.Equal(t, []string{}, splitAddrs(""))
	require.Equal(t, []string{"/ip4/1.2.3.4/tcp/4001/p2p/a", "/ip4/1.2.3.4/udp/4001/quic-v1/p2p/a"},
		splitAddrs(" /ip4/1.2.3.4/tcp/4001/p2p/a ,, /ip4/1.2.3.4/udp/4001/quic-v1/p2p/a,"))
}
