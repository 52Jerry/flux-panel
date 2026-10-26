//go:build linux

package tcpstats

import (
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const platformSupported = true

func probeConn(conn net.Conn) (observation, error) {
	// TLS exposes its underlying socket via NetConn. Other supported forwarding
	// wrappers propagate syscall.Conn; userspace WireGuard does not provide one.
	for i := 0; i < 8; i++ {
		if sc, ok := conn.(syscall.Conn); ok {
			raw, err := sc.SyscallConn()
			if err == nil && raw != nil {
				return probeRaw(raw)
			}
		}
		if wrapped, ok := conn.(interface{ NetConn() net.Conn }); ok {
			conn = wrapped.NetConn()
			if conn != nil {
				continue
			}
		}
		break
	}
	return observation{}, errUnavailable
}

func probeRaw(raw syscall.RawConn) (result observation, err error) {
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		var info unix.TCPInfo
		length := uint32(unsafe.Sizeof(info))
		// Preserve optlen: older kernels return a shorter TCP_INFO structure.
		// The x/sys typed helper discards that length and zero-fills absent fields.
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.IPPROTO_TCP, unix.TCP_INFO,
			uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&length)), 0)
		if errno != 0 {
			socketErr = errno
			return
		}
		result = decodeInfo(&info, length)
		if cc, e := unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION); e == nil {
			result.congestionControl = strings.TrimRight(cc, "\x00")
		}
	})
	if err != nil {
		return observation{}, err
	}
	return result, socketErr
}

func decodeInfo(info *unix.TCPInfo, length uint32) observation {
	available := func(offset, size uintptr) bool { return uint64(length) >= uint64(offset+size) }
	var result observation
	if available(unsafe.Offsetof(info.Rtt), unsafe.Sizeof(info.Rtt)) {
		result.rttMicros = info.Rtt
	}
	if available(unsafe.Offsetof(info.Min_rtt), unsafe.Sizeof(info.Min_rtt)) {
		result.minRTTMicros = info.Min_rtt
	}
	if available(unsafe.Offsetof(info.Total_retrans), unsafe.Sizeof(info.Total_retrans)) {
		result.hasRetransmissions = true
		result.retransmissions = info.Total_retrans
	}
	if available(unsafe.Offsetof(info.Notsent_bytes), unsafe.Sizeof(info.Notsent_bytes)) {
		result.hasNotsentBytes = true
		result.notsentBytes = info.Notsent_bytes
	}
	if available(unsafe.Offsetof(info.Delivery_rate), unsafe.Sizeof(info.Delivery_rate)) {
		result.deliveryBytesPerSecond = info.Delivery_rate
	}
	return result
}
