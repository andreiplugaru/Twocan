package p2p

import (
	"errors"
	"net"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"

	"twocan/internal/logging"
)

func addrStringsOf(addrs []ma.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

func TestWithInterfaceIPs(t *testing.T) {
	ips := func() ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("192.168.1.5"),
			net.ParseIP("fe80::1"),
			net.ParseIP("2001:db8::5"),
		}, nil
	}
	f := withInterfaceIPs(ips, logging.Discard())
	got := f([]ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/tcp/4001"),
		ma.StringCast("/ip6/::1/udp/5000/quic-v1"),
		ma.StringCast("/ip4/203.0.113.7/tcp/9"), // observed: left alone
	})
	assert.ElementsMatch(t, []string{
		"/ip4/127.0.0.1/tcp/4001",
		"/ip6/::1/udp/5000/quic-v1",
		"/ip4/203.0.113.7/tcp/9",
		"/ip4/192.168.1.5/tcp/4001",
		"/ip6/2001:db8::5/udp/5000/quic-v1",
	}, addrStringsOf(got))
}

func TestWithInterfaceIPsError(t *testing.T) {
	f := withInterfaceIPs(func() ([]net.IP, error) { return nil, errors.New("netlinkrib: permission denied") }, logging.Discard())
	in := []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/4001")}
	assert.Equal(t, []string{"/ip4/127.0.0.1/tcp/4001"}, addrStringsOf(f(in)))
}

func TestWithInterfaceIPsCaches(t *testing.T) {
	calls := 0
	f := withInterfaceIPs(func() ([]net.IP, error) {
		calls++
		return []net.IP{net.ParseIP("10.0.0.2")}, nil
	}, logging.Discard())
	in := []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")}
	f(in)
	f(in)
	assert.Equal(t, 1, calls)
}
