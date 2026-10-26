package net

import (
	"errors"
	"io"
	stdnet "net"
	"os"
	"sync"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/x/internal/net/tcpstats"
	"github.com/go-gost/x/internal/net/transportstats"
)

const bufferSize = 64 * 1024
const streamBufferSize = 32 * 1024

// DefaultWriteTimeout applies to forward/relay handlers, not the legacy API.
const DefaultWriteTimeout = 60 * time.Second

type TransportOptions struct {
	// WriteTimeout bounds a pending network write without progress. Zero disables
	// it; a connection waiting for application data has no pending write.
	WriteTimeout time.Duration
	// IdleTimeout is optional and measures inactivity across BOTH directions.
	IdleTimeout time.Duration
}

// Transport retains the legacy API without imposing a new default timeout.
func Transport(rw1, rw2 io.ReadWriter) error {
	return TransportWithOptions(rw1, rw2, TransportOptions{})
}

// TransportWithIdleTimeout retains the seconds-based API. Activity in either
// direction keeps the connection alive; blocked writes also have a bound.
func TransportWithIdleTimeout(rw1, rw2 io.ReadWriter, idleTimeout int) error {
	d := time.Duration(idleTimeout) * time.Second
	return TransportWithOptions(rw1, rw2, TransportOptions{IdleTimeout: d, WriteTimeout: d})
}

// TransportWithOptions closes both endpoints on completion and waits for both
// copy workers. Endpoints must implement Close which interrupts pending I/O,
// as net.Conn does. No detached per-write goroutine or data queue is created.
func TransportWithOptions(rw1, rw2 io.ReadWriter, opts TransportOptions) error {
	transportstats.TransferStarted()
	defer transportstats.TransferFinished()
	defer tcpstats.Track(rw1, rw2)()
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			if c, ok := rw1.(io.Closer); ok {
				_ = c.Close()
			}
			if c, ok := rw2.(io.Closer); ok {
				_ = c.Close()
			}
		})
	}
	idle := newIdleWatch(opts.IdleTimeout, closeBoth)
	defer idle.stop()
	writeOpts := WriteOptions{Timeout: opts.WriteTimeout, OnProgress: func(n int) {
		transportstats.BytesForwarded(n)
		idle.progress()
	}}
	errc := make(chan error, 2)
	size := bufferSize
	if streamEndpoint(rw1) && streamEndpoint(rw2) {
		size = streamBufferSize
	}
	copyOne := func(dst io.Writer, src io.Reader) {
		err := copyTransport(dst, src, writeOpts, idle.progress, size)
		closeBoth()
		errc <- err
	}
	go copyOne(rw1, rw2)
	copyOne(rw2, rw1)
	var err error
	for i := 0; i < 2; i++ {
		if e := <-errc; e != nil && e != io.EOF {
			// Keep the initiating timeout rather than a consequential closed pipe.
			if err == nil || errors.Is(e, os.ErrDeadlineExceeded) {
				err = e
			}
		}
	}
	if idle.expired() {
		return &transportTimeoutError{operation: "idle", timeout: opts.IdleTimeout}
	}
	return err
}

func streamEndpoint(rw io.ReadWriter) bool {
	c, ok := rw.(stdnet.Conn)
	if !ok {
		return false
	}
	addr := c.RemoteAddr()
	if addr == nil {
		return false
	}
	switch addr.Network() {
	case "tcp", "tcp4", "tcp6", "unix", "pipe":
		return true
	}
	return false
}

func copyTransport(dst io.Writer, src io.Reader, opts WriteOptions, onRead func(), size int) error {
	buf := bufpool.Get(size)
	defer bufpool.Put(buf)
	emptyReads := 0
	for {
		n, err := src.Read(buf)
		if n < 0 || n > len(buf) {
			return errors.New("invalid reader byte count")
		}
		if n > 0 {
			emptyReads = 0
			onRead()
			written, writeErr := WriteWithOptions(dst, buf[:n], opts)
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		} else if err == nil {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
		}
		if err != nil {
			return err
		}
	}
}

// CopyBuffer remains available for callers not using transport options.
func CopyBuffer(dst io.Writer, src io.Reader, bufSize int) error {
	buf := bufpool.Get(bufSize)
	defer bufpool.Put(buf)
	_, err := io.CopyBuffer(dst, src, buf)
	return err
}

type idleWatch struct {
	mu                sync.Mutex
	timer             *time.Timer
	last              time.Time
	timeout           time.Duration
	stopped, timedOut bool
	interrupt         func()
}

func newIdleWatch(timeout time.Duration, interrupt func()) *idleWatch {
	w := &idleWatch{last: time.Now(), timeout: timeout, interrupt: interrupt}
	if timeout > 0 {
		w.mu.Lock()
		w.timer = time.AfterFunc(timeout, w.check)
		w.mu.Unlock()
	}
	return w
}

func (w *idleWatch) progress() {
	if w.timeout <= 0 {
		return
	}
	w.mu.Lock()
	w.last = time.Now()
	w.mu.Unlock()
}

func (w *idleWatch) check() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	remaining := w.timeout - time.Since(w.last)
	if remaining > 0 {
		w.timer.Reset(remaining)
		w.mu.Unlock()
		return
	}
	w.timedOut = true
	w.mu.Unlock()
	w.interrupt()
}

func (w *idleWatch) stop() {
	w.mu.Lock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
}

func (w *idleWatch) expired() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.timedOut }
