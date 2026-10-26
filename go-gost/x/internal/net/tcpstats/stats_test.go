package tcpstats

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type testConn struct{ port int }

func (*testConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*testConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*testConn) Close() error                     { return nil }
func (c *testConn) LocalAddr() net.Addr            { return &net.TCPAddr{Port: c.port} }
func (c *testConn) RemoteAddr() net.Addr           { return &net.TCPAddr{Port: c.port + 1} }
func (*testConn) SetDeadline(time.Time) error      { return nil }
func (*testConn) SetReadDeadline(time.Time) error  { return nil }
func (*testConn) SetWriteDeadline(time.Time) error { return nil }

func TestBoundedRegistryReleasesReferencesAndRotatesSamples(t *testing.T) {
	var ports []int
	r := newRegistry(3, 2, func(c net.Conn) (observation, error) {
		ports = append(ports, c.(*testConn).port)
		return observation{rttMicros: 1000}, nil
	}, true)
	done := []func(){r.track(&testConn{1}), r.track(&testConn{2}), r.track(&testConn{3}), r.track(&testConn{4})}
	if s := r.snapshot(); s.TrackedConnections != 3 || s.SampledConnections != 2 || !s.SamplingLimited {
		t.Fatalf("unexpected bounded sample: %+v", s)
	}
	r.snapshot()
	if len(ports) != 4 || ports[0] != 1 || ports[1] != 2 || ports[2] != 3 || ports[3] != 1 {
		t.Fatalf("sampling did not rotate: %v", ports)
	}
	for _, release := range done {
		release()
		release()
	}
	if s := r.snapshot(); s.TrackedConnections != 0 || s.SamplingLimited || len(r.entries) != 0 || r.overflow != 0 {
		t.Fatalf("closed flows retained: %+v", s)
	}
}

func TestSampleMetricsHaveExplicitUnitsAndNoUnsupportedZeroes(t *testing.T) {
	r := newRegistry(4, 4, func(c net.Conn) (observation, error) {
		switch c.(*testConn).port {
		case 1:
			return observation{rttMicros: 10000, minRTTMicros: 7000, retransmissions: 2, notsentBytes: 100, deliveryBytesPerSecond: 1000, congestionControl: "bbr", hasRetransmissions: true, hasNotsentBytes: true}, nil
		case 2:
			return observation{rttMicros: 30000, minRTTMicros: 9000, retransmissions: 3, notsentBytes: 200, deliveryBytesPerSecond: 2000, congestionControl: "cubic", hasRetransmissions: true, hasNotsentBytes: true}, nil
		case 3:
			return observation{}, errUnavailable
		default:
			return observation{}, errors.New("closed socket")
		}
	}, true)
	for i := 1; i <= 4; i++ {
		r.track(&testConn{i})
	}
	s := r.snapshot()
	if s.SampledConnections != 2 || s.UnavailableConnections != 1 || s.SampleErrors != 1 || *s.RTTMillisAvg != 20 || *s.MinRTTMillis != 7 || *s.Retransmissions != 5 || *s.NotsentBytes != 300 || *s.DeliveryRateBPS != 24000 || *s.BBRConnections != 1 || *s.CubicConnections != 1 {
		t.Fatalf("wrong metric units or aggregation: %+v", s)
	}
	u := newRegistry(1, 1, func(net.Conn) (observation, error) { return observation{}, errUnavailable }, false)
	u.track(&testConn{5})
	s = u.snapshot()
	if s.SamplingSupported || s.RTTMillisAvg != nil || s.MinRTTMillis != nil || s.Retransmissions != nil || s.NotsentBytes != nil || s.DeliveryRateBPS != nil || s.BBRConnections != nil {
		t.Fatalf("unsupported metrics must remain unavailable: %+v", s)
	}
}

func TestShortKernelSamplesDoNotBecomeHealthyZeroCounters(t *testing.T) {
	r := newRegistry(1, 1, func(net.Conn) (observation, error) { return observation{rttMicros: 12000}, nil }, true)
	r.track(&testConn{1})
	s := r.snapshot()
	if s.SampledConnections != 1 || s.RTTMillisAvg == nil || *s.RTTMillisAvg != 12 || s.Retransmissions != nil || s.NotsentBytes != nil {
		t.Fatalf("absent TCP_INFO fields must be null: %+v", s)
	}
}

func TestTrackReleaseIsIdempotentAndDoesNotTreatPipesAsKernelTCP(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	before := Snapshot().TrackedConnections
	release := Track(a, b)
	if Snapshot().TrackedConnections != before {
		t.Fatal("in-memory pipe registered as TCP")
	}
	release()
	release()
	if Snapshot().TrackedConnections != before {
		t.Fatal("release changed unrelated observations")
	}
}
