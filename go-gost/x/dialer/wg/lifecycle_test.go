package wg

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/dialer"
)

type failingUnderlay struct{}

func (failingUnderlay) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("underlay unavailable")
}

func TestWireGuardUnderlayFailure(t *testing.T) {
	d := newTestDialer(t, validMetadata(t))
	if _, err := d.Dial(context.Background(), "127.0.0.1:51820", dialer.NetDialerDialOption(failingUnderlay{})); err == nil {
		t.Fatal("underlay failure was ignored")
	}
	if len(d.tunnels) != 0 {
		t.Fatal("failed device remained in the pool")
	}
}

func TestWireGuardCancelledWhileWaitingForDevice(t *testing.T) {
	d := newTestDialer(t, validMetadata(t))
	d.lock <- struct{}{}
	defer func() { <-d.lock }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.Dial(ctx, "127.0.0.1:51820")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation blocked behind initialization")
	}
}

func TestWireGuardCloseDuringConnect(t *testing.T) {
	p := newPeer(t)
	d := newTestDialer(t, p.metadata)
	u := &trackedDialer{}
	lease, err := d.Dial(context.Background(), p.endpoint, dialer.NetDialerDialOption(u))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := lease.(*sessionConn).Connect(context.Background(), "tcp", "10.77.0.99:80")
		result <- err
	}()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed connection unexpectedly connected")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Connect")
	}
	if u.closes.Load() != 1 || len(d.tunnels) != 0 {
		t.Fatal("Close leaked or double-released the device")
	}
}
