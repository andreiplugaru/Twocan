package p2p

import (
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// bootstrapAddrs is a comma-separated list of helper multiaddrs baked in at
// build time, e.g. -ldflags "-X twocan/internal/p2p.bootstrapAddrs=/ip4/…/p2p/…".
// Release builds set it from the TWOCAN_BOOTSTRAP repository variable.
var bootstrapAddrs string

// DefaultBootstrap lists the multiaddrs (each ending in /p2p/<id>) of the
// bootstrap/relay helpers new nodes connect to.
var DefaultBootstrap = splitAddrs(bootstrapAddrs)

// splitAddrs splits a comma-separated list, dropping blank entries.
func splitAddrs(s string) []string {
	out := []string{}
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// ParseBootstrap parses bootstrap multiaddrs into AddrInfos. Blank strings are
// skipped; any other string that is not a multiaddr with a /p2p/ component is
// an error.
func ParseBootstrap(addrs []string) ([]peer.AddrInfo, error) {
	var parsed []peer.AddrInfo
	for _, s := range addrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		info, err := peer.AddrInfoFromString(s)
		if err != nil {
			return nil, fmt.Errorf("parse bootstrap addr %q: %w", s, err)
		}
		parsed = append(parsed, *info)
	}
	return parsed, nil
}
