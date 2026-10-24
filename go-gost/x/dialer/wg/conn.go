package wg

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type tunnel struct {
	device *device.Device
	stack  *netstack.Net
	md     metadata
	refs   int // protected by wgDialer.lock
}

// sessionConn owns a reference to the tunnel from Dial until Close, including
// failed/cancelled connects. The last reference closes the device and UDP bind.
type sessionConn struct {
	mu                 sync.Mutex
	tunnel             *tunnel
	peer               net.Addr
	release            func()
	conn               net.Conn
	connecting, closed bool
	cancel             context.CancelFunc
}

func (c *sessionConn) Connect(ctx context.Context, network, address string) (result net.Conn, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if c.connecting || c.conn != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("wg: session is already connected")
	}
	c.connecting = true
	ctx, cancel := context.WithTimeout(ctx, c.tunnel.md.dialTimeout)
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return nil, fmt.Errorf("wg: unsupported network %q", network)
	}
	host, port, err := net.SplitHostPort(address)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("wg: target must be host:port with a port between 1 and 65535")
	}
	var ips []string
	if ip, e := netip.ParseAddr(host); e == nil {
		ips = []string{ip.Unmap().String()}
	} else {
		if len(c.tunnel.md.dns) == 0 {
			return nil, fmt.Errorf("wg: dns is required for hostname targets")
		}
		ips, err = c.tunnel.stack.LookupContextHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("wg: resolve target through tunnel: %w", err)
		}
	}
	err = fmt.Errorf("wg: target is outside allowedIPs or has no matching address family")
	for _, s := range ips {
		ip, e := netip.ParseAddr(s)
		if e != nil || ip.Zone() != "" || !c.tunnel.md.allows(ip) {
			continue
		}
		if (network[len(network)-1] == '4' && !ip.Is4()) || (network[len(network)-1] == '6' && ip.Is4()) {
			continue
		}
		var cc net.Conn
		cc, err = c.tunnel.stack.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err != nil {
			continue
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			cc.Close()
			return nil, net.ErrClosed
		}
		c.conn = cc
		c.mu.Unlock()
		if network[:3] == "udp" {
			return &packetConn{sessionConn: c}, nil
		}
		return c, nil
	}
	return nil, err
}

func (c *sessionConn) connected() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	if c.conn == nil {
		return nil, fmt.Errorf("wg: use the wg connector to select a target")
	}
	return c.conn, nil
}

func (c *sessionConn) Read(p []byte) (int, error) {
	cc, err := c.connected()
	if err != nil {
		return 0, err
	}
	return cc.Read(p)
}
func (c *sessionConn) Write(p []byte) (int, error) {
	cc, err := c.connected()
	if err != nil {
		return 0, err
	}
	return cc.Write(p)
}
func (c *sessionConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	cc := c.conn
	c.mu.Unlock()
	var err error
	if cc != nil {
		err = cc.Close()
	}
	c.release()
	return err
}
func (c *sessionConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.LocalAddr()
	}
	return &net.IPAddr{IP: c.tunnel.md.addresses[0].AsSlice()}
}
func (c *sessionConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return c.peer
}
func (c *sessionConn) SetDeadline(t time.Time) error {
	cc, err := c.connected()
	if err != nil {
		return err
	}
	return cc.SetDeadline(t)
}
func (c *sessionConn) SetReadDeadline(t time.Time) error {
	cc, err := c.connected()
	if err != nil {
		return err
	}
	return cc.SetReadDeadline(t)
}
func (c *sessionConn) SetWriteDeadline(t time.Time) error {
	cc, err := c.connected()
	if err != nil {
		return err
	}
	return cc.SetWriteDeadline(t)
}
func (c *sessionConn) CloseWrite() error {
	cc, err := c.connected()
	if err != nil {
		return err
	}
	if cw, ok := cc.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return fmt.Errorf("wg: connection does not support CloseWrite")
}

// GOST's UDP forwarders use net.PacketConn. A connected UDP session must never
// silently send a WriteTo packet to a different destination.
type packetConn struct{ *sessionConn }

func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Read(p)
	return n, c.RemoteAddr(), err
}
func (c *packetConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if addr == nil || addr.String() != c.RemoteAddr().String() {
		return 0, fmt.Errorf("wg: UDP destination differs from connected target")
	}
	return c.Write(p)
}
