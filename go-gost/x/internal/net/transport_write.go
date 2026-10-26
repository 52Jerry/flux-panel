package net

import (
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/go-gost/x/internal/net/transportstats"
)

// WriteOptions passes through byte-counting and rate-limit wrappers. Limiter
// waits happen BEFORE invoking WriteWithOptions on the next layer. OnProgress
// is called exactly once at the deepest layer for each positive Write result.
type WriteOptions struct {
	Timeout    time.Duration
	OnProgress func(int)
}

type OptionsWriter interface {
	WriteWithOptions([]byte, WriteOptions) (int, error)
}
type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

func (c *readWriteConn) WriteWithOptions(p []byte, opts WriteOptions) (int, error) {
	return WriteWithOptions(c.w, p, opts)
}

func (c *readWriteConn) NetConn() stdnet.Conn { return c.Conn }

func (c *readWriteConn) Close() error {
	// The writer may be a rate-limited io.ReadWriter separate from Conn. Its
	// token wait must be canceled even though Conn owns the actual socket.
	if pending, ok := c.w.(interface{ CancelPending() }); ok {
		pending.CancelPending()
	}
	if pending, ok := c.r.(interface{ CancelPending() }); ok {
		pending.CancelPending()
	}
	return c.Conn.Close()
}

type transportTimeoutError struct {
	operation string
	timeout   time.Duration
}

func (e *transportTimeoutError) Error() string {
	return fmt.Sprintf("transport %s made no progress for %s", e.operation, e.timeout)
}
func (*transportTimeoutError) Timeout() bool   { return true }
func (*transportTimeoutError) Temporary() bool { return true }
func (*transportTimeoutError) Unwrap() error   { return os.ErrDeadlineExceeded }

// WriteWithOptions bounds continuous lack of write progress. Short, retryable
// deadlines expose partial writes; positive results refresh the budget. One
// watchdog also closes deadline-less or no-op-deadline connections. No goroutine
// is spawned to run Write. As with net.Conn, Close must interrupt blocked I/O.
func WriteWithOptions(dst io.Writer, p []byte, opts WriteOptions) (total int, resultErr error) {
	if writer, ok := dst.(OptionsWriter); ok {
		return writer.WriteWithOptions(p, opts)
	}
	if len(p) == 0 {
		return 0, nil
	}
	deadline, supportsDeadline := dst.(writeDeadliner)
	closer, supportsClose := dst.(io.Closer)
	// UDP listeners can share a descriptor across logical peers.
	if c, ok := dst.(stdnet.Conn); ok && c.RemoteAddr() != nil && c.RemoteAddr().Network() == "udp" {
		supportsDeadline = false
	}
	if opts.Timeout > 0 && !supportsClose && !supportsDeadline {
		return 0, errors.New("write timeout requires a closeable or deadline-aware writer")
	}
	interrupt := func() {
		if supportsClose {
			closer.Close()
		} else if supportsDeadline {
			deadline.SetWriteDeadline(time.Now())
		}
	}
	watch := newWriteWatch(opts.Timeout, interrupt)
	defer func() {
		timedOut := watch.stop()
		var timeout interface{ Timeout() bool }
		if !timedOut && opts.Timeout > 0 && errors.As(resultErr, &timeout) && timeout.Timeout() {
			// A native deadline can win the race with the fallback timer.
			transportstats.WriteTimedOut()
			timedOut = true
		}
		if timedOut {
			resultErr = &transportTimeoutError{operation: "write", timeout: opts.Timeout}
		}
	}()
	poll := writeStallThreshold / 2
	if opts.Timeout > 0 && opts.Timeout/4 < poll {
		poll = opts.Timeout / 4
	}
	if poll <= 0 {
		poll = time.Nanosecond
	}
	// Only raw TCP and net.Pipe can safely retry probing deadlines. TLS and
	// some framed protocols permanently poison a connection after a timeout.
	_, probeDeadline := dst.(*stdnet.TCPConn)
	typeOfWriter := reflect.TypeOf(dst)
	if typeOfWriter.Kind() == reflect.Pointer && typeOfWriter.Elem().PkgPath() == "net" && typeOfWriter.Elem().Name() == "pipe" {
		probeDeadline = true
	}
	if !probeDeadline {
		poll = opts.Timeout
	}
	useDeadline := opts.Timeout > 0 && supportsDeadline
	if opts.Timeout > 0 && !useDeadline {
		transportstats.DeadlineUnsupported()
	}
	if useDeadline {
		defer deadline.SetWriteDeadline(time.Time{})
	}
	for len(p) > 0 {
		if useDeadline {
			if err := deadline.SetWriteDeadline(time.Now().Add(poll)); err != nil {
				useDeadline = false
				transportstats.DeadlineUnsupported()
				if !supportsClose {
					return total, fmt.Errorf("write deadline unsupported: %w", err)
				}
			}
		}
		started := time.Now()
		n, err := dst.Write(p)
		if n < 0 || n > len(p) {
			return total, errors.New("invalid writer byte count")
		}
		total += n
		p = p[n:]
		if n > 0 {
			watch.progress()
			if opts.OnProgress != nil {
				opts.OnProgress(n)
			}
		}
		if watch.expired() {
			return total, &transportTimeoutError{operation: "write", timeout: opts.Timeout}
		}
		if err != nil {
			var timeout interface{ Timeout() bool }
			if useDeadline && probeDeadline && errors.As(err, &timeout) && timeout.Timeout() {
				// Defective deadline wrappers must not create a tight retry loop.
				backoff := min(poll, 10*time.Millisecond)
				if n == 0 && time.Since(started) < backoff {
					time.Sleep(backoff - time.Since(started))
				}
				continue
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

const writeStallThreshold = time.Second

// Only an in-flight operation is retained. Timers update live aggregate metrics
// at a one-second cadence and are stopped on every exit path.
type writeWatch struct {
	mu                         sync.Mutex
	timer                      *time.Timer
	started, progressAt        time.Time
	timeout                    time.Duration
	stopped, stalled, timedOut bool
	interrupt                  func()
	interruptDone              chan struct{}
}

func newWriteWatch(timeout time.Duration, interrupt func()) *writeWatch {
	now := time.Now()
	w := &writeWatch{started: now, progressAt: now, timeout: timeout, interrupt: interrupt, interruptDone: make(chan struct{})}
	transportstats.WriteStarted()
	delay := writeStallThreshold
	if timeout > 0 && timeout < delay {
		delay = timeout
	}
	w.mu.Lock()
	w.timer = time.AfterFunc(delay, w.check)
	w.mu.Unlock()
	return w
}

func (w *writeWatch) check() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	age := time.Since(w.progressAt)
	transportstats.ObserveBlock(age)
	if age >= writeStallThreshold && !w.stalled {
		w.stalled = true
		transportstats.StallStarted()
	}
	if w.timeout > 0 && age >= w.timeout {
		w.timedOut = true
		transportstats.WriteTimedOut()
		w.mu.Unlock()
		w.interrupt()
		close(w.interruptDone)
		return
	}
	delay := writeStallThreshold
	if w.timeout > 0 && w.timeout-age < delay {
		delay = w.timeout - age
	}
	if !w.stalled && writeStallThreshold-age < delay {
		delay = writeStallThreshold - age
	}
	if delay <= 0 {
		delay = time.Millisecond
	}
	w.timer.Reset(delay)
	w.mu.Unlock()
}

func (w *writeWatch) progress() {
	w.mu.Lock()
	transportstats.ObserveBlock(time.Since(w.progressAt))
	w.progressAt = time.Now()
	if w.stalled {
		w.stalled = false
		transportstats.StallFinished()
	}
	w.mu.Unlock()
}

func (w *writeWatch) stop() bool {
	w.mu.Lock()
	w.stopped = true
	w.timer.Stop()
	transportstats.ObserveBlock(time.Since(w.progressAt))
	transportstats.WriteFinished(time.Since(w.started))
	if w.stalled {
		w.stalled = false
		transportstats.StallFinished()
	}
	expired := w.timedOut
	w.mu.Unlock()
	// A timer already committed to closing the endpoint must finish before
	// returning; it cannot close a successfully reused connection afterward.
	if expired {
		<-w.interruptDone
	}
	return expired
}

func (w *writeWatch) expired() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.timedOut }
