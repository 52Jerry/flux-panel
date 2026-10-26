package net_test

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
	"github.com/go-gost/core/observer/stats"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/net/transportstats"
	trafficwrapper "github.com/go-gost/x/limiter/traffic/wrapper"
	xstats "github.com/go-gost/x/observer/stats"
	statswrapper "github.com/go-gost/x/observer/stats/wrapper"
)

type pacedLimiter struct {
	delay   time.Duration
	entered chan struct{}
	once    sync.Once
}

func (l *pacedLimiter) Wait(ctx context.Context, n int) int {
	l.once.Do(func() { close(l.entered) })
	timer := time.NewTimer(l.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0
	case <-timer.C:
		return min(n, 1)
	}
}
func (*pacedLimiter) Limit() int                                                       { return 1 }
func (*pacedLimiter) Set(int)                                                          {}
func (*pacedLimiter) In(context.Context, string, ...limiter.Option) traffic.Limiter    { return nil }
func (l *pacedLimiter) Out(context.Context, string, ...limiter.Option) traffic.Limiter { return l }

func TestRelayWrapperChainExcludesPacingFromWriteTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	lim := &pacedLimiter{delay: 100 * time.Millisecond, entered: make(chan struct{})}
	s := xstats.NewStats(false)
	rw := statswrapper.WrapReadWriter(trafficwrapper.WrapReadWriter(lim, a, "client"), s)
	c := xnet.NewReadWriteConn(rw, rw, a)
	defer c.Close()
	done := make(chan error, 1)
	var progress atomic.Int64
	before := transportstats.Snapshot()
	start := time.Now()
	go func() {
		_, err := xnet.WriteWithOptions(c, []byte("abc"), xnet.WriteOptions{Timeout: 40 * time.Millisecond, OnProgress: func(n int) { progress.Add(int64(n)) }})
		done <- err
	}()
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(b, buf); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pacing counted as socket stall: %v", err)
	}
	if string(buf) != "abc" || progress.Load() != 3 || s.Get(stats.KindOutputBytes) != 3 {
		t.Fatalf("wrong bytes/progress/stats %q/%d/%d", buf, progress.Load(), s.Get(stats.KindOutputBytes))
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("pacing was bypassed")
	}
	if transportstats.Snapshot().WriteTimeoutsTotal != before.WriteTimeoutsTotal {
		t.Fatal("healthy pacing added a timeout")
	}
}

func TestRelayWrapperChainCloseCancelsPendingTokens(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	lim := &pacedLimiter{delay: time.Hour, entered: make(chan struct{})}
	rw := statswrapper.WrapReadWriter(trafficwrapper.WrapReadWriter(lim, a, "client"), xstats.NewStats(false))
	c := xnet.NewReadWriteConn(rw, rw, a)
	done := make(chan error, 1)
	go func() {
		_, err := xnet.WriteWithOptions(c, []byte("x"), xnet.WriteOptions{Timeout: 20 * time.Millisecond})
		done <- err
	}()
	select {
	case <-lim.entered:
	case <-time.After(time.Second):
		t.Fatal("token wait not entered")
	}
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("token wait survived readWriteConn.Close")
	}
}
