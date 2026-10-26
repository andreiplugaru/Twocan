package core

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/libp2p/go-libp2p/core/host"
	ma "github.com/multiformats/go-multiaddr"

	"twocan/internal/store"
)

// Listening ports. For a listen address with port 0, libp2p picks a random
// port. Twocan keeps the port it got: contacts save our addresses (and our
// card carries them), so a new port on every start would leave them
// dialling one nobody listens on. Each start asks for the ports of the last
// run; a port that is taken by now gets a new random one, which is kept
// from then on.

// savedListenAddrs returns the listen addresses saveListenAddrs stored.
func savedListenAddrs(ctx context.Context, st *store.Store) []ma.Multiaddr {
	v, _, err := st.GetSetting(ctx, store.SettingListenAddrs)
	if err != nil || v == "" {
		return nil
	}
	var out []ma.Multiaddr
	for _, s := range strings.Split(v, ",") {
		if a, err := ma.NewMultiaddr(s); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// withSavedPorts returns listen with each port-0 address replaced by the
// saved address of the same form (see portless), if there is one.
func withSavedPorts(listen []string, saved []ma.Multiaddr) []string {
	out := slices.Clone(listen)
	for i, l := range listen {
		la, err := ma.NewMultiaddr(l)
		if err != nil || portless(la) != la.String() {
			continue // not port 0: libp2p reports a bad address itself
		}
		for _, s := range saved {
			if portless(s) == l {
				out[i] = s.String()
				break
			}
		}
	}
	return out
}

// relistenTaken listens again, on a random port, for each address of listen
// that was asked for at a saved port (tried differs from listen) that
// libp2p could not get.
func relistenTaken(h host.Host, listen, tried []string, log *slog.Logger) {
	bound := map[string]bool{}
	for _, a := range h.Network().ListenAddresses() {
		bound[portless(a)] = true
	}
	for i, l := range listen {
		if tried[i] == l || bound[l] {
			continue
		}
		log.Info("saved listen port taken; using a new one", "addr", tried[i])
		if err := h.Network().Listen(ma.StringCast(l)); err != nil {
			log.Warn("could not listen", "addr", l, "err", err)
		}
	}
}

// saveListenAddrs stores the addresses we listen on for listen's port-0
// addresses, to ask for at the next start.
func saveListenAddrs(ctx context.Context, st *store.Store, h host.Host, listen []string, log *slog.Logger) {
	var keep []string
	for _, a := range h.Network().ListenAddresses() {
		if slices.Contains(listen, portless(a)) {
			keep = append(keep, a.String())
		}
	}
	if err := st.SetSetting(ctx, store.SettingListenAddrs, strings.Join(keep, ",")); err != nil {
		log.Warn("could not save listen addresses", "err", err)
	}
}

// portless is a with its TCP or UDP port set to 0: the form it was asked
// for in, e.g. /ip4/0.0.0.0/udp/0/quic-v1 for /ip4/0.0.0.0/udp/4242/quic-v1.
func portless(a ma.Multiaddr) string {
	var b strings.Builder
	for _, c := range a {
		v := c.Value()
		if code := c.Code(); code == ma.P_TCP || code == ma.P_UDP {
			v = "0"
		}
		b.WriteString("/" + c.Protocol().Name)
		if v != "" {
			b.WriteString("/" + v)
		}
	}
	return b.String()
}
