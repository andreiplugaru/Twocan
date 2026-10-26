//go:build !android

package mobile

import "net"

// interfaceIPs is nil off Android: libp2p lists the interfaces itself.
var interfaceIPs func() ([]net.IP, error)
