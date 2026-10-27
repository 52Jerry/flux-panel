//go:build linux

package tcp

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// Configure the listener before bind/listen. Accepted sockets inherit this
// choice; outbound dials and other services keep their own defaults.
func configureCongestionControl(lc *net.ListenConfig, algorithm string) error {
	if algorithm == "" {
		return nil
	}
	lc.Control = func(network, address string, raw syscall.RawConn) error {
		var socketErr error
		if err := raw.Control(func(fd uintptr) {
			socketErr = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, algorithm)
		}); err != nil {
			return err
		}
		if socketErr != nil {
			return fmt.Errorf("set TCP congestion control %s: %w", algorithm, socketErr)
		}
		return nil
	}
	return nil
}
