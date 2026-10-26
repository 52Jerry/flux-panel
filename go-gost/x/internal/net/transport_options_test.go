package net

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/net/transportstats"
	"github.com/go-gost/x/metadata"
)

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	server, err := ln.AcceptTCP()
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func TestTransportWriteTimeoutRealTCP(t *testing.T) {
	client, inbound := tcpPair(t)
	outbound, upstream := tcpPair(t)
	_ = outbound.SetWriteBuffer(1024)
	_ = upstream.SetReadBuffer(1024)
	before := transportstats.Snapshot()
	done := make(chan error, 1)
	go func() {
		done <- TransportWithOptions(inbound, outbound, TransportOptions{WriteTimeout: 150 * time.Millisecond})
	}()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		buf := make([]byte, 64*1024)
		for {
			if _, err := client.Write(buf); err != nil {
				return
			}
		}
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("expected write timeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TCP receiver that never reads kept transfer blocked")
	}
	client.Close()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("sender remained blocked")
	}
	after := transportstats.Snapshot()
	if after.ActiveTransfers != before.ActiveTransfers || after.ActiveStalledWrites != before.ActiveStalledWrites {
		t.Fatalf("active metrics retained: before=%+v after=%+v", before, after)
	}
	if after.WriteTimeoutsTotal <= before.WriteTimeoutsTotal || after.BytesForwardedTotal <= before.BytesForwardedTotal {
		t.Fatalf("missing timeout/forwarding counters: %+v", after)
	}
}

type noWriteDeadlineConn struct {
	net.Conn
	noOp bool
}

func (c *noWriteDeadlineConn) SetWriteDeadline(time.Time) error {
	if c.noOp {
		return nil
	}
	return errors.New("not supported")
}

func TestWriteTimeoutWithoutWorkingDeadline(t *testing.T) {
	for _, noOp := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "no-op"}[noOp], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			done := make(chan error, 1)
			go func() {
				_, err := WriteWithOptions(&noWriteDeadlineConn{Conn: a, noOp: noOp}, []byte("blocked"), WriteOptions{Timeout: 80 * time.Millisecond})
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close fallback failed to interrupt Write")
			}
		})
	}
}

func TestWriteProgressRefreshesTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	var progress atomic.Int64
	go func() {
		_, err := WriteWithOptions(a, make([]byte, 16), WriteOptions{Timeout: 160 * time.Millisecond, OnProgress: func(n int) { progress.Add(int64(n)) }})
		done <- err
	}()
	for i := 0; i < 16; i++ {
		time.Sleep(25 * time.Millisecond)
		if _, err := io.ReadFull(b, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("healthy partial writes timed out: %v", err)
	}
	if progress.Load() != 16 {
		t.Fatalf("progress counted %d, want 16", progress.Load())
	}
}

func TestTLSWritesDoNotUsePoisoningProbeDeadlines(t *testing.T) {
	// TLS permanently records a write timeout. A short progress probe must
	// never poison a healthy TLS stream before its configured budget expires.
	certServer := httptest.NewTLSServer(nil)
	defer certServer.Close()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	server := tls.Server(a, &tls.Config{Certificates: certServer.TLS.Certificates})
	client := tls.Client(b, &tls.Config{InsecureSkipVerify: true}) // local fixture only
	handshake := make(chan error, 1)
	go func() { handshake <- server.Handshake() }()
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-handshake; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := WriteWithOptions(client, []byte("ok"), WriteOptions{Timeout: 200 * time.Millisecond})
		done <- err
	}()
	time.Sleep(80 * time.Millisecond)
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(server, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("TLS write poisoned by a probe: %v", err)
	}
}

func TestWriteTimeoutDoesNotCloseHealthyIdleConnection(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	done := make(chan error, 1)
	go func() {
		done <- TransportWithOptions(inbound, outbound, TransportOptions{WriteTimeout: 40 * time.Millisecond})
	}()
	select {
	case err := <-done:
		t.Fatalf("idle connection closed without pending write: %v", err)
	case <-time.After(160 * time.Millisecond):
	}
	sent := make(chan error, 1)
	go func() { _, err := client.Write([]byte("ok")); sent <- err }()
	if _, err := io.ReadFull(upstream, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transfer not cleaned up")
	}
}

func TestIdleTimeoutTracksBothDirectionsTogether(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	done := make(chan error, 1)
	go func() {
		done <- TransportWithOptions(inbound, outbound, TransportOptions{IdleTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond})
	}()
	for i := 0; i < 10; i++ {
		sent := make(chan error, 1)
		go func() { _, err := client.Write([]byte("x")); sent <- err }()
		if _, err := io.ReadFull(upstream, make([]byte, 1)); err != nil {
			t.Fatalf("one-way active stream: %v", err)
		}
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("inactive reverse direction closed healthy stream: %v", err)
	default:
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle timer failed after all progress stopped")
	}
}

func TestWriteTimeoutZeroDisablesProtection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() { _, err := WriteWithOptions(a, []byte("x"), WriteOptions{}); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("disabled timeout returned early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt disabled timeout writer")
	}
}

func TestWriteStallMetricsRelease(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	before := transportstats.Snapshot()
	done := make(chan error, 1)
	go func() {
		_, err := WriteWithOptions(a, []byte("x"), WriteOptions{Timeout: 1500 * time.Millisecond})
		done <- err
	}()
	deadline := time.Now().Add(1400 * time.Millisecond)
	for transportstats.Snapshot().WriteStallsTotal == before.WriteStallsTotal && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	during := transportstats.Snapshot()
	if during.ActiveStalledWrites != before.ActiveStalledWrites+1 || during.MaxWriteBlockMillis < 1000 {
		t.Fatalf("missing active stall: %+v", during)
	}
	b.Close()
	<-done
	after := transportstats.Snapshot()
	if after.ActiveStalledWrites != before.ActiveStalledWrites {
		t.Fatalf("stall retained: %+v", after)
	}
}

func TestTransportTimeoutMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  time.Duration
		bad   bool
	}{
		{"zero", 0, 0, false}, {"json-zero", float64(0), 0, false}, {"json-seconds", float64(1.5), 1500 * time.Millisecond, false},
		{"duration", "250ms", 250 * time.Millisecond, false}, {"numeric-string", "2", 2 * time.Second, false},
		{"negative", -1, 0, true}, {"invalid", "broken", 0, true}, {"null", nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TransportTimeout(metadata.NewMetadata(map[string]any{"writeTimeout": tc.value}), "writeTimeout", DefaultWriteTimeout)
			if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
				t.Fatalf("got %s/%v", got, err)
			}
		})
	}
	got, err := TransportTimeout(nil, "writeTimeout", DefaultWriteTimeout)
	if err != nil || got != 60*time.Second {
		t.Fatalf("default %s/%v", got, err)
	}
}
