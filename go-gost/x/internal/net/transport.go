package net

import (
	"io"
	"time"

	"github.com/go-gost/core/common/bufpool"
)

const (
	bufferSize = 64 * 1024
)

// Transport performs bidirectional copy between two connections.
// When either direction completes, it immediately closes the other side
// to interrupt the remaining goroutine, then waits for it to finish.
// This prevents goroutine and file descriptor leaks when one peer
// becomes unresponsive (e.g. dead residential SOCKS proxy).
func Transport(rw1, rw2 io.ReadWriter) error {
	errc := make(chan error, 2) // buffer 2: both goroutines can always send

	go func() {
		err := CopyBuffer(rw1, rw2, bufferSize)
		// Close the other side to interrupt the second goroutine immediately.
		if c, ok := rw2.(io.Closer); ok {
			c.Close()
		}
		errc <- err
	}()

	go func() {
		err := CopyBuffer(rw2, rw1, bufferSize)
		if c, ok := rw1.(io.Closer); ok {
			c.Close()
		}
		errc <- err
	}()

	var err error
	for i := 0; i < 2; i++ {
		if e := <-errc; e != nil && e != io.EOF {
			err = e
		}
	}
	return err
}

// TransportWithIdleTimeout is like Transport but applies a per-read idle
// deadline. If no data arrives on either connection within idleTimeout
// seconds, the blocked Read() returns a deadline-exceeded error which
// triggers the half-close logic in Transport.
func TransportWithIdleTimeout(rw1, rw2 io.ReadWriter, idleTimeout int) error {
	if idleTimeout <= 0 {
		return Transport(rw1, rw2)
	}

	d := time.Duration(idleTimeout) * time.Second
	w1 := &idleDeadlineConn{rw: rw1, timeout: d}
	w2 := &idleDeadlineConn{rw: rw2, timeout: d}

	return Transport(w1, w2)
}

// idleDeadlineConn wraps an io.ReadWriter and sets a read deadline before
// every Read(). If the underlying object implements SetReadDeadline
// (net.Conn does), the deadline is enforced by the OS, causing the Read
// to return after the timeout even if the peer sends nothing.
type idleDeadlineConn struct {
	rw     io.ReadWriter
	timeout time.Duration
}

func (w *idleDeadlineConn) Read(p []byte) (int, error) {
	if c, ok := w.rw.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = c.SetReadDeadline(time.Now().Add(w.timeout))
	}
	n, err := w.rw.Read(p)
	if c, ok := w.rw.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = c.SetReadDeadline(time.Time{})
	}
	return n, err
}

func (w *idleDeadlineConn) Write(p []byte) (int, error) {
	return w.rw.Write(p)
}

func (w *idleDeadlineConn) Close() error {
	if c, ok := w.rw.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// CopyBuffer copies from src to dst using a pooled buffer.
func CopyBuffer(dst io.Writer, src io.Reader, bufSize int) error {
	buf := bufpool.Get(bufSize)
	defer bufpool.Put(buf)

	_, err := io.CopyBuffer(dst, src, buf)
	return err
}
