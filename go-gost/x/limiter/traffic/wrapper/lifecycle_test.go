package wrapper

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
)

type blockingTrafficLimiter struct {
	entered chan struct{}
	once    sync.Once
}

func (l *blockingTrafficLimiter) Wait(ctx context.Context, _ int) int {
	l.once.Do(func() { close(l.entered) })
	<-ctx.Done()
	return 0
}
func (*blockingTrafficLimiter) Limit() int { return 1 }
func (*blockingTrafficLimiter) Set(int)    {}
func (l *blockingTrafficLimiter) In(context.Context, string, ...limiter.Option) traffic.Limiter {
	return l
}
func (l *blockingTrafficLimiter) Out(context.Context, string, ...limiter.Option) traffic.Limiter {
	return l
}

func TestCloseCancelsTCPRateWait(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		t.Run(direction, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			lim := &blockingTrafficLimiter{entered: make(chan struct{})}
			conn := WrapConn(left, lim, "test")
			done := make(chan error, 1)
			if direction == "read" {
				go right.Write([]byte("payload"))
				go func() { _, err := conn.Read(make([]byte, 16)); done <- err }()
			} else {
				go func() { _, err := conn.Write([]byte("payload")); done <- err }()
			}
			select {
			case <-lim.entered:
			case <-time.After(time.Second):
				t.Fatal("rate wait did not start")
			}
			conn.Close()
			conn.Close()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled I/O succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("rate wait survived Close")
			}
		})
	}
}

func TestCloseCancelsUDPRateWait(t *testing.T) {
	for _, kind := range []string{"packet", "udp"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			lim := &blockingTrafficLimiter{entered: make(chan struct{})}
			var conn net.PacketConn
			if kind == "packet" {
				conn = WrapPacketConn(raw, lim, "test")
			} else {
				conn = WrapUDPConn(raw, lim, "test")
			}
			done := make(chan error, 1)
			go func() { _, err := conn.WriteTo([]byte("payload"), raw.LocalAddr()); done <- err }()
			select {
			case <-lim.entered:
			case <-time.After(time.Second):
				t.Fatal("rate wait did not start")
			}
			conn.Close()
			conn.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("UDP rate wait survived Close")
			}
		})
	}
}
