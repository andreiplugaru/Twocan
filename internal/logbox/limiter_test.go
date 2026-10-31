package logbox

import (
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestLimiterPerPeer(t *testing.T) {
	l := newLimiter(6, 3, time.Hour)
	now := time.Now()
	for range 3 {
		require.True(t, l.allow("1.2.3.4", "peer", now))
	}
	require.False(t, l.allow("1.2.3.4", "peer", now))
	require.True(t, l.allow("1.2.3.4", "peer", now.Add(time.Hour+time.Second)))
}

func TestLimiterPerIP(t *testing.T) {
	l := newLimiter(6, 3, time.Hour)
	now := time.Now()
	for i := range 6 {
		require.True(t, l.allow("1.2.3.4", peer.ID(fmt.Sprint("p", i)), now))
	}
	require.False(t, l.allow("1.2.3.4", "p7", now))
	require.True(t, l.allow("5.6.7.8", "p7", now), "another address is counted apart")
}

func TestLimiterConcurrency(t *testing.T) {
	l := newLimiter(6, 3, time.Hour)
	require.True(t, l.acquire())
	require.True(t, l.acquire())
	require.False(t, l.acquire())
	l.release()
	require.True(t, l.acquire())
}
