package wrapper

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/go-gost/core/limiter"
	traffic "github.com/go-gost/core/limiter/traffic"
	xnet "github.com/go-gost/x/internal/net"
	xtraffic "github.com/go-gost/x/limiter/traffic"
)

type fixedRate struct{ lim traffic.Limiter }

func (f fixedRate) In(context.Context, string, ...core.Option) traffic.Limiter  { return f.lim }
func (f fixedRate) Out(context.Context, string, ...core.Option) traffic.Limiter { return f.lim }

func TestPolicyWaitLongerThanNetworkTimeoutRemainsHealthy(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	lim := xtraffic.NewLimiter(10)
	defer lim.(io.Closer).Close()
	conn := WrapConn(left, fixedRate{lim}, "test").(*limitConn)
	defer conn.Close()
	received := make(chan error, 1)
	go func() { _, err := io.ReadFull(right, make([]byte, 3)); received <- err }()
	var progress atomic.Int64
	started := time.Now()
	n, err := conn.WriteWithOptions([]byte("abc"), xnet.WriteOptions{Timeout: 30 * time.Millisecond, OnProgress: func(n int) { progress.Add(int64(n)) }})
	if err != nil || n != 3 || progress.Load() != 3 {
		t.Fatalf("healthy paced write failed: %d %v, progress %d", n, err, progress.Load())
	}
	if time.Since(started) < 150*time.Millisecond {
		t.Fatal("rate waiting was skipped")
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}

func TestSlowSocketDoesNotHoldSharedUserScheduler(t *testing.T) {
	provider := xtraffic.NewTrafficLimiter(xtraffic.LimitsOption("$ 10000B 10000B"))
	defer provider.(io.Closer).Close()
	slowLeft, slowRight := net.Pipe()
	defer slowRight.Close()
	fastLeft, fastRight := net.Pipe()
	defer fastRight.Close()
	slow := WrapConn(slowLeft, provider, "$", core.ScopeOption(core.ScopeService), core.ServiceOption("1_1_1_tcp")).(*limitConn)
	defer slow.Close()
	fast := WrapConn(fastLeft, provider, "$", core.ScopeOption(core.ScopeService), core.ServiceOption("2_2_2_tcp")).(*limitConn)
	defer fast.Close()
	slowDone := make(chan error, 1)
	go func() {
		_, err := slow.WriteWithOptions(make([]byte, 1000), xnet.WriteOptions{Timeout: 400 * time.Millisecond})
		slowDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	go io.ReadFull(fastRight, make([]byte, 100))
	started := time.Now()
	n, err := fast.WriteWithOptions(make([]byte, 100), xnet.WriteOptions{Timeout: 100 * time.Millisecond})
	if err != nil || n != 100 || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("slow peer blocked another user: %d %v", n, err)
	}
	select {
	case <-slowDone:
		t.Fatal("slow peer was not still blocked")
	default:
	}
}

func TestDatagramLargerThanPacingBurstIsDeliveredWhole(t *testing.T) {
	for _, kind := range []string{"packet", "udp"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			lim := xtraffic.NewLimiter(1000)
			defer lim.(io.Closer).Close()
			var conn net.PacketConn
			if kind == "packet" {
				conn = WrapPacketConn(raw, fixedRate{lim}, "test")
			} else {
				conn = WrapUDPConn(raw, fixedRate{lim}, "test")
			}
			defer conn.Close()
			payload := make([]byte, 300)
			for i := range payload {
				payload[i] = byte(i)
			}
			started := time.Now()
			n, err := conn.WriteTo(payload, raw.LocalAddr())
			if err != nil || n != len(payload) {
				t.Fatalf("datagram write %d %v", n, err)
			}
			if time.Since(started) < 150*time.Millisecond {
				t.Fatal("whole datagram was not charged")
			}
			raw.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 512)
			n, _, err = raw.ReadFrom(buf)
			if err != nil || n != len(payload) || string(buf[:n]) != string(payload) {
				t.Fatalf("datagram fragmented/dropped: %d %v", n, err)
			}
		})
	}
}

func TestReadWriterCloseInterruptsPolicyWait(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	lim := &blockingTrafficLimiter{entered: make(chan struct{})}
	rw := WrapReadWriter(lim, left, "test").(*readWriter)
	done := make(chan error, 1)
	go func() { _, err := rw.Write([]byte("payload")); done <- err }()
	select {
	case <-lim.entered:
	case <-time.After(time.Second):
		t.Fatal("wait not started")
	}
	rw.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("readWriter wait survived close")
	}
}

type eofReadWriter struct{ payload []byte }

func (r *eofReadWriter) Read(p []byte) (int, error) {
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		return n, io.EOF
	}
	return n, nil
}
func (r *eofReadWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestPartialEOFIsChargedAndBufferedBytesSurviveRateDisable(t *testing.T) {
	lim := xtraffic.NewLimiter(1000)
	defer lim.(io.Closer).Close()
	rw := WrapReadWriter(fixedRate{lim}, &eofReadWriter{payload: make([]byte, 200)}, "test").(*readWriter)
	defer rw.Close()
	buf := make([]byte, 512)
	n, err := rw.Read(buf)
	if n != 100 || err != nil {
		t.Fatalf("partial EOF bypassed pacing or dropped tail: %d %v", n, err)
	}
	lim.Set(0)
	n, err = rw.Read(buf)
	if n != 100 || err != io.EOF {
		t.Fatalf("buffered EOF tail lost on update: %d %v", n, err)
	}
}

func TestConnectedUDPPolicyWaitIsOutsideNetworkTimeout(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	raw, err := net.DialUDP("udp4", nil, peer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	lim := xtraffic.NewLimiter(1000)
	defer lim.(io.Closer).Close()
	conn := WrapUDPConn(raw, fixedRate{lim}, "test").(*udpConn)
	defer conn.Close()
	var progress atomic.Int64
	start := time.Now()
	n, err := conn.WriteWithOptions(make([]byte, 300), xnet.WriteOptions{Timeout: 30 * time.Millisecond, OnProgress: func(n int) { progress.Add(int64(n)) }})
	if err != nil || n != 300 || progress.Load() != 300 {
		t.Fatalf("paced UDP failed: %d %v, progress %d", n, err, progress.Load())
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("packet was not fully charged")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err = peer.ReadFromUDP(make([]byte, 512))
	if err != nil || n != 300 {
		t.Fatalf("packet split/lost: %d %v", n, err)
	}
}
