package wrapper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
	"github.com/go-gost/core/metadata"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/net/udp"
)

var (
	errUnsupport = errors.New("unsupported operation")
)

type connLifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

func newConnLifetime() connLifetime {
	ctx, cancel := context.WithCancel(context.Background())
	return connLifetime{ctx: ctx, cancel: cancel}
}

func (l *connLifetime) close(conn io.Closer) error {
	l.once.Do(func() {
		// A blocked limiter is outside the underlying socket's deadline/Close.
		l.cancel()
		l.err = conn.Close()
	})
	return l.err
}

func (l *connLifetime) wait(limiter traffic.Limiter, n int) int {
	limit := limiter.Wait(l.ctx, n)
	if l.ctx.Err() != nil {
		return 0
	}
	return limit
}

// limitConn is a Conn with traffic limiter supported.
type limitConn struct {
	net.Conn
	life    connLifetime
	rbuf    bytes.Buffer
	limiter traffic.TrafficLimiter
	opts    []limiter.Option
	key     string
}

func WrapConn(c net.Conn, tlimiter traffic.TrafficLimiter, key string, opts ...limiter.Option) net.Conn {
	if tlimiter == nil {
		return c
	}

	return &limitConn{
		Conn:    c,
		life:    newConnLifetime(),
		limiter: tlimiter,
		opts:    opts,
		key:     key,
	}
}

func (c *limitConn) Close() error { return c.life.close(c.Conn) }

func (c *limitConn) Read(b []byte) (n int, err error) {
	limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
	if limiter == nil || limiter.Limit() <= 0 {
		return c.Conn.Read(b)
	}

	if c.rbuf.Len() > 0 {
		burst := len(b)
		if c.rbuf.Len() < burst {
			burst = c.rbuf.Len()
		}
		lim := c.life.wait(limiter, burst)
		if c.life.ctx.Err() != nil {
			return 0, net.ErrClosed
		}
		return c.rbuf.Read(b[:lim])
	}

	nn, err := c.Conn.Read(b)
	if err != nil {
		return nn, err
	}

	n = c.life.wait(limiter, nn)
	if c.life.ctx.Err() != nil {
		return 0, net.ErrClosed
	}
	if n < nn {
		if _, err = c.rbuf.Write(b[n:nn]); err != nil {
			return 0, err
		}
	}

	return
}

func (c *limitConn) Write(b []byte) (n int, err error) {
	limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
	if limiter == nil || limiter.Limit() <= 0 {
		return c.Conn.Write(b)
	}

	nn := 0
	for len(b) > 0 {
		limit := c.life.wait(limiter, len(b))
		if c.life.ctx.Err() != nil {
			return n, net.ErrClosed
		}
		if limit <= 0 {
			return n, io.ErrNoProgress
		}
		nn, err = c.Conn.Write(b[:limit])
		n += nn
		if err != nil {
			return
		}
		b = b[nn:]
	}

	return
}

func (c *limitConn) SyscallConn() (rc syscall.RawConn, err error) {
	if sc, ok := c.Conn.(syscall.Conn); ok {
		rc, err = sc.SyscallConn()
		return
	}
	err = errUnsupport
	return
}

func (c *limitConn) Metadata() metadata.Metadata {
	if md, ok := c.Conn.(metadata.Metadatable); ok {
		return md.Metadata()
	}
	return nil
}

type packetConn struct {
	net.PacketConn
	life    connLifetime
	limiter traffic.TrafficLimiter
	opts    []limiter.Option
	key     string
}

func WrapPacketConn(pc net.PacketConn, lim traffic.TrafficLimiter, key string, opts ...limiter.Option) net.PacketConn {
	if lim == nil {
		return pc
	}
	return &packetConn{
		PacketConn: pc,
		life:       newConnLifetime(),
		limiter:    lim,
		opts:       opts,
		key:        key,
	}
}

func (c *packetConn) Close() error { return c.life.close(c.PacketConn) }

func (c *packetConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	for {
		n, addr, err = c.PacketConn.ReadFrom(p)
		if err != nil {
			return
		}

		limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
		if limiter == nil || limiter.Limit() <= 0 {
			return
		}

		// discard when exceed the limit size.
		if c.life.wait(limiter, n) < n {
			continue
		}

		return
	}
}

func (c *packetConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	// discard when exceed the limit size.
	limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
	if limiter != nil && limiter.Limit() > 0 &&
		c.life.wait(limiter, len(p)) < len(p) {
		n = len(p)
		return
	}

	return c.PacketConn.WriteTo(p, addr)
}

func (c *packetConn) Metadata() metadata.Metadata {
	if md, ok := c.PacketConn.(metadata.Metadatable); ok {
		return md.Metadata()
	}
	return nil
}

type udpConn struct {
	net.PacketConn
	life    connLifetime
	limiter traffic.TrafficLimiter
	opts    []limiter.Option
	key     string
}

func WrapUDPConn(pc net.PacketConn, limiter traffic.TrafficLimiter, key string, opts ...limiter.Option) udp.Conn {
	return &udpConn{
		PacketConn: pc,
		life:       newConnLifetime(),
		limiter:    limiter,
		opts:       opts,
		key:        key,
	}
}

func (c *udpConn) Close() error { return c.life.close(c.PacketConn) }

func (c *udpConn) RemoteAddr() net.Addr {
	if nc, ok := c.PacketConn.(xnet.RemoteAddr); ok {
		return nc.RemoteAddr()
	}
	return nil
}

func (c *udpConn) SetReadBuffer(n int) error {
	if nc, ok := c.PacketConn.(xnet.SetBuffer); ok {
		return nc.SetReadBuffer(n)
	}
	return errUnsupport
}

func (c *udpConn) SetWriteBuffer(n int) error {
	if nc, ok := c.PacketConn.(xnet.SetBuffer); ok {
		return nc.SetWriteBuffer(n)
	}
	return errUnsupport
}

func (c *udpConn) Read(b []byte) (n int, err error) {
	nc, ok := c.PacketConn.(io.Reader)
	if !ok {
		err = errUnsupport
		return
	}

	for {
		n, err = nc.Read(b)
		if err != nil {
			return
		}

		if c.limiter == nil {
			return
		}

		limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
		if limiter == nil || limiter.Limit() <= 0 {
			return
		}

		// discard when exceed the limit size.
		if c.life.wait(limiter, n) < n {
			continue
		}

		return
	}
}

func (c *udpConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	for {
		n, addr, err = c.PacketConn.ReadFrom(p)
		if err != nil {
			return
		}

		if c.limiter == nil {
			return
		}

		limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
		if limiter == nil || limiter.Limit() <= 0 {
			return
		}

		// discard when exceed the limit size.
		if c.life.wait(limiter, n) < n {
			continue
		}

		return
	}
}

func (c *udpConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	nc, ok := c.PacketConn.(udp.ReadUDP)
	if !ok {
		err = errUnsupport
		return
	}

	for {
		n, addr, err = nc.ReadFromUDP(b)
		if err != nil {
			return
		}

		if c.limiter == nil {
			return
		}

		limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
		if limiter == nil || limiter.Limit() <= 0 {
			return
		}

		// discard when exceed the limit size.
		if c.life.wait(limiter, n) < n {
			continue
		}

		return
	}
}

func (c *udpConn) ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error) {
	nc, ok := c.PacketConn.(udp.ReadUDP)
	if !ok {
		err = errUnsupport
		return
	}

	for {
		n, oobn, flags, addr, err = nc.ReadMsgUDP(b, oob)
		if err != nil {
			return
		}

		if c.limiter == nil {
			return
		}

		limiter := c.limiter.In(c.life.ctx, c.key, c.opts...)
		if limiter == nil || limiter.Limit() <= 0 {
			return
		}

		// discard when exceed the limit size.
		if c.life.wait(limiter, n) < n {
			continue
		}
		return
	}
}

func (c *udpConn) Write(p []byte) (n int, err error) {
	nc, ok := c.PacketConn.(io.Writer)
	if !ok {
		err = errUnsupport
		return
	}

	if c.limiter != nil {
		// discard when exceed the limit size.
		limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
		if limiter != nil && limiter.Limit() > 0 &&
			c.life.wait(limiter, len(p)) < len(p) {
			n = len(p)
			return
		}
	}

	n, err = nc.Write(p)
	return
}

func (c *udpConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if c.limiter != nil {
		// discard when exceed the limit size.
		limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
		if limiter != nil && limiter.Limit() > 0 &&
			c.life.wait(limiter, len(p)) < len(p) {
			n = len(p)
			return
		}
	}

	n, err = c.PacketConn.WriteTo(p, addr)
	return
}

func (c *udpConn) WriteToUDP(p []byte, addr *net.UDPAddr) (n int, err error) {
	nc, ok := c.PacketConn.(udp.WriteUDP)
	if !ok {
		err = errUnsupport
		return
	}

	if c.limiter != nil {
		// discard when exceed the limit size.
		limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
		if limiter != nil && limiter.Limit() > 0 &&
			c.life.wait(limiter, len(p)) < len(p) {
			n = len(p)
			return
		}
	}

	n, err = nc.WriteToUDP(p, addr)
	return
}

func (c *udpConn) WriteMsgUDP(p, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	nc, ok := c.PacketConn.(udp.WriteUDP)
	if !ok {
		err = errUnsupport
		return
	}

	if c.limiter != nil {
		// discard when exceed the limit size.
		limiter := c.limiter.Out(c.life.ctx, c.key, c.opts...)
		if limiter != nil && limiter.Limit() > 0 &&
			c.life.wait(limiter, len(p)) < len(p) {
			n = len(p)
			return
		}
	}

	n, oobn, err = nc.WriteMsgUDP(p, oob, addr)
	return
}

func (c *udpConn) SyscallConn() (rc syscall.RawConn, err error) {
	if nc, ok := c.PacketConn.(xnet.SyscallConn); ok {
		return nc.SyscallConn()
	}
	err = errUnsupport
	return
}

func (c *udpConn) SetDSCP(n int) error {
	if nc, ok := c.PacketConn.(xnet.SetDSCP); ok {
		return nc.SetDSCP(n)
	}
	return nil
}

func (c *udpConn) Metadata() metadata.Metadata {
	if md, ok := c.PacketConn.(metadata.Metadatable); ok {
		return md.Metadata()
	}
	return nil
}
