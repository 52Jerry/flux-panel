package service

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	xstats "github.com/go-gost/x/observer/stats"
)

type lifecycleListener struct {
	net.Listener
	closes atomic.Int32
}

func (*lifecycleListener) Init(metadata.Metadata) error { return nil }
func (l *lifecycleListener) Close() error               { l.closes.Add(1); return l.Listener.Close() }

type lifecycleHandler struct{ entered chan context.Context }

func (*lifecycleHandler) Init(metadata.Metadata) error { return nil }
func (h *lifecycleHandler) Handle(ctx context.Context, conn net.Conn, _ ...handler.HandleOption) error {
	h.entered <- ctx
	_, err := io.Copy(io.Discard, conn)
	return err
}

func newLifecycleService(t *testing.T, opts ...Option) (*defaultService, *lifecycleListener, *lifecycleHandler) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycleListener{Listener: ln}
	h := &lifecycleHandler{entered: make(chan context.Context, 1)}
	s := NewService("lifecycle-test", l, h, opts...).(*defaultService)
	t.Cleanup(func() { s.Close() })
	return s, l, h
}

func TestCloseStopsActiveConnectionsAndIsIdempotent(t *testing.T) {
	s, ln, h := newLifecycleService(t)
	go s.Serve()
	client, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var ctx context.Context
	select {
	case ctx = <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	start := time.Now()
	s.Close()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("Close waited instead of terminating active TCP")
	}
	if ctx.Err() == nil {
		t.Fatal("handler context was not cancelled")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("Serve still running")
	}
	if ln.closes.Load() != 1 {
		t.Fatalf("listener closed %d times", ln.closes.Load())
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted connection survived close")
	}
}

func TestCloseBeforeServeDoesNotWaitAndServeCannotRestart(t *testing.T) {
	s, ln, _ := newLifecycleService(t)
	start := time.Now()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("unstarted service close waited for Serve")
	}
	if err := s.Serve(); err != net.ErrClosed {
		t.Fatalf("Serve after close: %v", err)
	}
	if ln.closes.Load() != 1 {
		t.Fatal("listener closed repeatedly")
	}
}

type cancellableObserver struct {
	entered chan struct{}
	exited  chan struct{}
}

func (o *cancellableObserver) Observe(ctx context.Context, events []observer.Event, _ ...observer.Option) error {
	if len(events) == 0 || events[0].Type() != observer.EventStats {
		return nil
	}
	close(o.entered)
	<-ctx.Done()
	close(o.exited)
	return ctx.Err()
}

func TestCloseCancelsStatsObserverBeforeWaiting(t *testing.T) {
	st := xstats.NewStats(false)
	st.Add(stats.KindTotalConns, 1)
	o := &cancellableObserver{entered: make(chan struct{}), exited: make(chan struct{})}
	s, _, _ := newLifecycleService(t, StatsOption(st), ObserverOption(o), ObserverPeriodOption(time.Second))
	go s.Serve()
	select {
	case <-o.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stats observer did not start")
	}
	s.Close()
	select {
	case <-o.exited:
	default:
		t.Fatal("observer context survived Close")
	}
}
