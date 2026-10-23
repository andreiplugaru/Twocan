package p2p

import (
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// DefaultBootstrap lists the multiaddrs (each ending in /p2p/<id>) of the
// bootstrap/relay helpers new nodes connect to.
var DefaultBootstrap = []string{}

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
