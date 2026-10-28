package mobile

import (
	"net"

	"github.com/wlynxg/anet"
)

// interfaceIPs lists the phone's IPs. Go's net.InterfaceAddrs fails on
// Android 11+ (apps may not bind netlink sockets); anet works around it.
// anet needs the -checklinkname=0 linker flag.
func interfaceIPs() ([]net.IP, error) {
	addrs, err := anet.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			ips = append(ips, ipnet.IP)
		}
	}
	return ips, nil
}
