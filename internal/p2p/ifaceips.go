package p2p

import (
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// ifaceIPsTTL is how long withInterfaceIPs reuses the IPs it was given.
// libp2p recomputes our addresses every few seconds; this keeps that from
// listing the interfaces each time while still noticing a network change.
const ifaceIPsTTL = 10 * time.Second

// withInterfaceIPs returns an addrs factory for Config.InterfaceIPs. libp2p
// turns a 0.0.0.0 or :: listen address into one address per local IP, but
// where it cannot list them (Android 11+ forbids the netlink calls it uses)
// it falls back to loopback only. For each loopback address, the factory
// adds the same address with every IP from ips of the same family that is
// neither loopback nor link-local, so peers on the LAN and the card get
// dialable addresses. If ips fails, addrs are returned unchanged.
func withInterfaceIPs(ips func() ([]net.IP, error), log *slog.Logger) func([]ma.Multiaddr) []ma.Multiaddr {
	var (
		mu     sync.Mutex
		cached []net.IP
		at     time.Time
	)
	current := func() []net.IP {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < ifaceIPsTTL {
			return cached
		}
		got, err := ips()
		if err != nil {
			log.Warn("list interface addresses", "err", err)
			got = nil
		}
		cached, at = got, time.Now()
		return cached
	}
	return func(addrs []ma.Multiaddr) []ma.Multiaddr {
		ips := current()
		out := slices.Clone(addrs)
		for _, a := range addrs {
			if !manet.IsIPLoopback(a) {
				continue
			}
			first, rest := ma.SplitFirst(a)
			v4 := first.Code() == ma.P_IP4
			for _, ip := range ips {
				if ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ip.To4() != nil) != v4 {
					continue
				}
				ipAddr, err := manet.FromIP(ip)
				if err != nil {
					continue
				}
				out = append(out, ipAddr.Encapsulate(rest))
			}
		}
		return ma.Unique(out)
	}
}
