package net

import (
	"bytes"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"
)

type transportDatagram struct {
	payload []byte
	written []byte
	closed  chan struct{}
	once    sync.Once
}

func (c *transportDatagram) Read(p []byte) (int, error) {
	if c.payload != nil {
		if len(p) < len(c.payload) {
			return 0, io.ErrShortBuffer
		}
		n := copy(p, c.payload)
		c.payload = nil
		return n, io.EOF
	}
	<-c.closed
	return 0, stdnet.ErrClosed
}
func (c *transportDatagram) Write(p []byte) (int, error) {
	c.written = append(c.written, p...)
	return len(p), nil
}
func (c *transportDatagram) Close() error                   { c.once.Do(func() { close(c.closed) }); return nil }
func (*transportDatagram) LocalAddr() stdnet.Addr           { return &stdnet.UDPAddr{} }
func (*transportDatagram) RemoteAddr() stdnet.Addr          { return &stdnet.UDPAddr{} }
func (*transportDatagram) SetDeadline(time.Time) error      { return nil }
func (*transportDatagram) SetReadDeadline(time.Time) error  { return nil }
func (*transportDatagram) SetWriteDeadline(time.Time) error { return nil }

func TestStreamMemoryOptimizationPreservesLargeDatagrams(t *testing.T) {
	payload := bytes.Repeat([]byte("d"), 65507)
	source := &transportDatagram{payload: payload, closed: make(chan struct{})}
	target := &transportDatagram{closed: make(chan struct{})}
	// Completion waits for both workers, so inspecting written is synchronized.
	_ = TransportWithOptions(source, target, TransportOptions{TCPBufferSize: 4096})
	if !bytes.Equal(target.written, payload) {
		t.Fatalf("datagram was truncated: received %d of %d bytes", len(target.written), len(payload))
	}
}
