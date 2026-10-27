package net

import (
	"fmt"
	"io"
	"net"
)

// setTCPSocketBuffers bounds kernel queues separately from the userspace copy
// buffer. Walk only a bounded number of transparent wrappers; never replace the
// wrapped I/O path, which owns accounting and traffic limits.
func setTCPSocketBuffers(rw io.ReadWriter, size int) error {
	conn, ok := rw.(net.Conn)
	if !ok {
		return nil
	}
	addr := conn.RemoteAddr()
	if addr == nil || (addr.Network() != "tcp" && addr.Network() != "tcp4" && addr.Network() != "tcp6") {
		return nil
	}
	for depth := 0; depth < 16 && conn != nil; depth++ {
		if socket, ok := conn.(*net.TCPConn); ok {
			if err := socket.SetReadBuffer(size); err != nil {
				return fmt.Errorf("TCP receive buffer: %w", err)
			}
			if err := socket.SetWriteBuffer(size); err != nil {
				return fmt.Errorf("TCP send buffer: %w", err)
			}
			return nil
		}
		wrapped, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			break
		}
		conn = wrapped.NetConn()
	}
	return fmt.Errorf("TCP socket buffer configuration requires a transparent native TCP connection")
}
