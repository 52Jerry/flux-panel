package wg

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// udpBind preserves GOST's selected route, interface and socket options instead
// of opening a separate socket outside the configured transport.
type udpBind struct {
	mu       sync.Mutex
	dial     func() (net.Conn, error)
	conn     net.Conn
	endpoint *endpoint
}

func (b *udpBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	if port != 0 {
		return nil, 0, fmt.Errorf("wg: fixed listen ports are not supported by the outbound bind")
	}
	c, err := b.dial()
	if err != nil {
		return nil, 0, err
	}
	b.conn = c
	// Keep the connection captured here: closing/reopening a bind must not move
	// an old receive loop onto the new connection.
	receive := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, err := c.Read(packets[0])
		if err != nil {
			return 0, err
		}
		sizes[0], eps[0] = n, b.endpoint
		return 1, nil
	}
	return []conn.ReceiveFunc{receive}, 0, nil
}

func (b *udpBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	return err
}

func (b *udpBind) SetMark(mark uint32) error {
	if mark != 0 {
		return fmt.Errorf("wg: set the socket mark on the GOST node instead")
	}
	return nil
}

func (b *udpBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	if ep != b.endpoint {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		return net.ErrClosed
	}
	for _, buf := range bufs {
		n, err := c.Write(buf)
		if err != nil {
			return err
		}
		if n != len(buf) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (b *udpBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	if s != b.endpoint.address {
		return nil, fmt.Errorf("wg: unexpected peer endpoint")
	}
	return b.endpoint, nil
}

func (*udpBind) BatchSize() int { return 1 }

type endpoint struct{ address string }

func (*endpoint) ClearSrc()             {}
func (*endpoint) SrcToString() string   { return "" }
func (e *endpoint) DstToString() string { return e.address }
func (e *endpoint) DstToBytes() []byte  { return []byte(e.address) }
func (e *endpoint) DstIP() netip.Addr {
	host, _, _ := net.SplitHostPort(e.address)
	ip, _ := netip.ParseAddr(host)
	return ip
}
func (*endpoint) SrcIP() netip.Addr { return netip.Addr{} }
func (*endpoint) Network() string   { return "udp" }
func (e *endpoint) String() string  { return e.address }
