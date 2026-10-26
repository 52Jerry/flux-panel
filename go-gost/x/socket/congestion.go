package socket

import (
	"github.com/go-gost/x/internal/net/tcpstats"
	"github.com/go-gost/x/internal/net/transportstats"
)

// CongestionInfo separates application write progress from a rotating sample of
// kernel TCP state. Neither forwarded bytes nor delivery rate is link capacity.
type CongestionInfo struct {
	ObservedAt                int64    `json:"observed_at"`
	ActiveTransfers           int64    `json:"active_transfers"`
	BytesForwardedTotal       uint64   `json:"bytes_forwarded_total"`
	WriteStallsTotal          uint64   `json:"write_stalls_total"`
	WriteTimeoutsTotal        uint64   `json:"write_timeouts_total"`
	ActiveStalledWrites       int64    `json:"active_stalled_writes"`
	MaxWriteBlockMillis       float64  `json:"max_write_block_ms"`
	TCPSamplingSupported      bool     `json:"tcp_sampling_supported"`
	TCPSamplingLimited        bool     `json:"tcp_sampling_limited"`
	TCPTrackedConnections     int      `json:"tcp_tracked_connections"`
	TCPSampledConnections     int      `json:"tcp_sampled_connections"`
	TCPUnavailableConnections int      `json:"tcp_unavailable_connections"`
	TCPSampleErrors           int      `json:"tcp_sample_errors"`
	TCPRTTMillisAvg           *float64 `json:"tcp_rtt_ms_avg"`
	TCPMinRTTMillis           *float64 `json:"tcp_min_rtt_ms"`
	TCPRetransmissions        *uint64  `json:"tcp_retransmissions"`
	TCPNotsentBytes           *uint64  `json:"tcp_notsent_bytes"`
	TCPDeliveryRateBPS        *uint64  `json:"tcp_delivery_rate_bps"`
	TCPBBRConnections         *uint64  `json:"tcp_bbr_connections"`
	TCPCubicConnections       *uint64  `json:"tcp_cubic_connections"`
}

func collectCongestionInfo() *CongestionInfo {
	transfers := transportstats.Snapshot()
	tcp := tcpstats.Snapshot()
	return &CongestionInfo{
		ObservedAt:                tcp.ObservedAt,
		ActiveTransfers:           int64(transfers.ActiveTransfers),
		BytesForwardedTotal:       uint64(transfers.BytesForwardedTotal),
		WriteStallsTotal:          uint64(transfers.WriteStallsTotal),
		WriteTimeoutsTotal:        uint64(transfers.WriteTimeoutsTotal),
		ActiveStalledWrites:       int64(transfers.ActiveStalledWrites),
		MaxWriteBlockMillis:       float64(transfers.MaxWriteBlockMillis),
		TCPSamplingSupported:      tcp.SamplingSupported,
		TCPSamplingLimited:        tcp.SamplingLimited,
		TCPTrackedConnections:     tcp.TrackedConnections,
		TCPSampledConnections:     tcp.SampledConnections,
		TCPUnavailableConnections: tcp.UnavailableConnections,
		TCPSampleErrors:           tcp.SampleErrors,
		TCPRTTMillisAvg:           tcp.RTTMillisAvg,
		TCPMinRTTMillis:           tcp.MinRTTMillis,
		TCPRetransmissions:        tcp.Retransmissions,
		TCPNotsentBytes:           tcp.NotsentBytes,
		TCPDeliveryRateBPS:        tcp.DeliveryRateBPS,
		TCPBBRConnections:         tcp.BBRConnections,
		TCPCubicConnections:       tcp.CubicConnections,
	}
}
