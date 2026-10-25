package core

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"

	"twocan/internal/helper"
	"twocan/internal/identity"
	"twocan/internal/p2p"
	"twocan/internal/store"
)

// TestChatThroughRelay: B listens on nothing and believes it is behind a NAT,
// so the only way to reach it is the helper's circuit relay. A knows only B's
// peer ID and must find B's relay address through the private DHT.
func TestChatThroughRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: real libp2p hosts on loopback")
	}

	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	// Autorelay builds circuit addrs only from a relay's public addrs, so the
	// loopback helper also advertises a public one. It is never dialled: A
	// and B reach the helper over their existing loopback connections.
	fakePublic := ma.StringCast("/ip4/1.2.3.4/tcp/4001")
	h, d, err := helper.Start(t.Context(), key, []string{"/ip4/127.0.0.1/tcp/0"},
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			return append(addrs, fakePublic)
		}))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, d.Close())
		require.NoError(t, h.Close())
	})
	var boot []string
	for _, addr := range h.Network().ListenAddresses() {
		if manet.IsIPLoopback(addr) { // skips the relay transport's /p2p-circuit
			boot = append(boot, addr.String()+"/p2p/"+h.ID().String())
		}
	}
	require.Len(t, boot, 1)

	startNode := func(listen []string, forcePrivate bool) *Node {
		n, err := Start(t.Context(), Options{
			DataDir:      t.TempDir(),
			Bootstrap:    boot,
			ListenAddrs:  listen,
			ForcePrivate: forcePrivate,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, n.Stop()) })
		return n
	}
	a := startNode([]string{"/ip4/127.0.0.1/tcp/0"}, false)
	b := startNode([]string{}, true)

	require.Eventually(t, func() bool {
		s, err := b.MyCard()
		if err != nil {
			return false
		}
		c, err := identity.DecodeCard(s)
		if err != nil {
			return false
		}
		for _, addr := range c.Addrs {
			if isCircuit(addr) {
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "B got no relay address")

	// A card with B's peer ID only: A has to look B up in the DHT.
	require.Empty(t, a.h.Peerstore().Addrs(b.PeerID()))
	card, err := identity.EncodeCard(identity.Card{PeerID: b.PeerID()})
	require.NoError(t, err)
	require.NoError(t, a.AddContact(t.Context(), card))
	require.Eventually(t, func() bool { return stateOf(b, a.PeerID()) == store.StatePendingIncoming },
		15*time.Second, poll, "contact request did not reach B")
	require.NoError(t, b.AcceptContact(t.Context(), a.PeerID()))
	require.Eventually(t, func() bool { return stateOf(a, b.PeerID()) == store.StateAccepted },
		15*time.Second, poll, "accept did not reach A")

	_, err = a.Send(t.Context(), b.PeerID(), "via relay")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		hist, err := b.History(t.Context(), a.PeerID())
		return err == nil && len(hist) == 1 && hist[0].Text == "via relay"
	}, 15*time.Second, poll, "message did not reach B")

	v := view(t, a, b.PeerID())
	require.True(t, v.Connected, "A not connected to B")
	require.True(t, v.Relayed, "A's connection to B is not relayed")

	det, err := a.ContactDetails(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.NotEmpty(t, det.Conns)
	for _, c := range det.Conns {
		require.True(t, c.Relayed)
		require.Equal(t, p2p.SourceHelper, c.Source, "A found B through the helper")
	}
	rtt, err := a.Ping(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.Positive(t, rtt)

	var reports int
	res, err := a.CheckConnection(t.Context(), b.PeerID(), func(int, p2p.Step) { reports++ })
	require.NoError(t, err)
	require.Positive(t, reports)
	require.True(t, res.Relayed)
	require.Contains(t, res.Verdict, "Connected through the helper's relay")
	for _, s := range res.Steps {
		require.NotEqual(t, p2p.StepFail, s.Status, "%s: %s", s.Title, s.Detail)
	}
}
