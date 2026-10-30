//go:build linux && !android

package core

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// watchNetlink calls changed whenever the kernel reports a link, address
// or route change, until ctx ends. Android forbids apps netlink sockets;
// there the app reports changes instead.
func watchNetlink(ctx context.Context, changed func()) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("netlink socket: %w", err)
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR |
		unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return fmt.Errorf("netlink bind: %w", err)
	}
	// Through os.File the read uses the runtime poller, so Close ends it.
	f := os.NewFile(uintptr(fd), "netlink")
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	buf := make([]byte, 64<<10)
	for {
		if _, err := f.Read(buf); err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return fmt.Errorf("netlink read: %w", err)
		}
		changed()
	}
}
