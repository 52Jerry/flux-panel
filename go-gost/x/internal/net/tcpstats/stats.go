// Package tcpstats provides bounded, rotating observations of active forwarding
// sockets. It never changes a socket's congestion control or retains closed flows.
package tcpstats

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	MaxTrackedConnections = 512
	MaxSamples            = 64
)

var errUnavailable = errors.New("kernel TCP information unavailable")

type observation struct {
	rttMicros              uint32
	minRTTMicros           uint32
	retransmissions        uint32
	notsentBytes           uint32
	deliveryBytesPerSecond uint64
	congestionControl      string
	hasRetransmissions     bool
	hasNotsentBytes        bool
}

// Stats describes one rotating sample, not node-wide loss or available capacity.
// Pointer fields are nil when no supported kernel socket supplied that metric.
type Stats struct {
	ObservedAt             int64
	SamplingSupported      bool
	SamplingLimited        bool
	TrackedConnections     int
	SampledConnections     int
	UnavailableConnections int
	SampleErrors           int
	RTTMillisAvg           *float64
	MinRTTMillis           *float64
	Retransmissions        *uint64
	NotsentBytes           *uint64
	DeliveryRateBPS        *uint64
	BBRConnections         *uint64
	CubicConnections       *uint64
}

type entry struct {
	id   uint64
	conn net.Conn
}

type registry struct {
	mu          sync.Mutex
	entries     []entry
	nextID      uint64
	cursor      int
	overflow    int
	limit       int
	sampleLimit int
	probe       func(net.Conn) (observation, error)
	supported   bool
}

func newRegistry(limit, samples int, probe func(net.Conn) (observation, error), supported bool) *registry {
	return &registry{limit: limit, sampleLimit: samples, probe: probe, supported: supported}
}

var active = newRegistry(MaxTrackedConnections, MaxSamples, probeConn, platformSupported)

// Track registers TCP endpoints for one bidirectional transport. The returned
// function must run when that transport exits, including all failure paths.
func Track(a, b io.ReadWriter) func() {
	var done []func()
	for _, rw := range []io.ReadWriter{a, b} {
		conn, ok := rw.(net.Conn)
		if !ok || conn.LocalAddr() == nil || !strings.HasPrefix(conn.LocalAddr().Network(), "tcp") {
			continue
		}
		done = append(done, active.track(conn))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, release := range done {
				release()
			}
		})
	}
}

func (r *registry) track(conn net.Conn) func() {
	r.mu.Lock()
	if len(r.entries) >= r.limit {
		r.overflow++
		r.mu.Unlock()
		var once sync.Once
		return func() { once.Do(func() { r.mu.Lock(); r.overflow--; r.mu.Unlock() }) }
	}
	r.nextID++
	id := r.nextID
	r.entries = append(r.entries, entry{id: id, conn: conn})
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			for i, item := range r.entries {
				if item.id == id {
					copy(r.entries[i:], r.entries[i+1:])
					r.entries[len(r.entries)-1] = entry{}
					r.entries = r.entries[:len(r.entries)-1]
					if r.cursor > i {
						r.cursor--
					}
					break
				}
			}
		})
	}
}

func Snapshot() Stats { return active.snapshot() }

func (r *registry) snapshot() Stats {
	r.mu.Lock()
	s := Stats{ObservedAt: time.Now().UnixMilli(), SamplingSupported: r.supported,
		TrackedConnections: len(r.entries), SamplingLimited: r.overflow > 0 || len(r.entries) > r.sampleLimit}
	count := min(len(r.entries), r.sampleLimit)
	samples := make([]entry, 0, count)
	for i := 0; i < count; i++ {
		samples = append(samples, r.entries[(r.cursor+i)%len(r.entries)])
	}
	if len(r.entries) > 0 {
		r.cursor = (r.cursor + count) % len(r.entries)
	}
	r.mu.Unlock()
	var rttSum, rttCount uint64
	var minRTT uint32
	var retrans, notsent, rate, bbr, cubic, ccSamples uint64
	var retransSamples, notsentSamples int
	for _, sample := range samples {
		info, err := r.probe(sample.conn)
		if err != nil {
			if errors.Is(err, errUnavailable) {
				s.UnavailableConnections++
			} else {
				s.SampleErrors++
			}
			continue
		}
		s.SampledConnections++
		if info.rttMicros > 0 {
			rttSum += uint64(info.rttMicros)
			rttCount++
		}
		if info.minRTTMicros > 0 && info.minRTTMicros != ^uint32(0) && (minRTT == 0 || info.minRTTMicros < minRTT) {
			minRTT = info.minRTTMicros
		}
		if info.hasRetransmissions {
			retrans += uint64(info.retransmissions)
			retransSamples++
		}
		if info.hasNotsentBytes {
			notsent += uint64(info.notsentBytes)
			notsentSamples++
		}
		rate = saturatingAdd(rate, saturatingMultiply8(info.deliveryBytesPerSecond))
		if info.congestionControl != "" {
			ccSamples++
			if strings.HasPrefix(info.congestionControl, "bbr") {
				bbr++
			}
			if info.congestionControl == "cubic" {
				cubic++
			}
		}
	}
	if s.SampledConnections > 0 {
		if rate > 0 {
			s.DeliveryRateBPS = &rate
		}
	}
	if retransSamples > 0 {
		s.Retransmissions = &retrans
	}
	if notsentSamples > 0 {
		s.NotsentBytes = &notsent
	}
	if ccSamples > 0 {
		s.BBRConnections, s.CubicConnections = &bbr, &cubic
	}
	if rttCount > 0 {
		value := float64(rttSum) / float64(rttCount) / 1000
		s.RTTMillisAvg = &value
	}
	if minRTT > 0 {
		value := float64(minRTT) / 1000
		s.MinRTTMillis = &value
	}
	return s
}

func saturatingMultiply8(n uint64) uint64 {
	if n > ^uint64(0)/8 {
		return ^uint64(0)
	}
	return n * 8
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}
