package socket

import (
	"encoding/json"
	"testing"
)

func TestCongestionPayloadPreservesUnavailableTCPValues(t *testing.T) {
	data, err := json.Marshal(SystemInfo{MemoryUsage: 1, Congestion: collectCongestionInfo()})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	var stats map[string]interface{}
	if err = json.Unmarshal(envelope["congestion"], &stats); err != nil {
		t.Fatal(err)
	}
	if stats["observed_at"].(float64) <= 0 {
		t.Fatal("timestamp missing")
	}
	for _, key := range []string{"tcp_rtt_ms_avg", "tcp_min_rtt_ms", "tcp_retransmissions", "tcp_notsent_bytes", "tcp_delivery_rate_bps"} {
		if value, ok := stats[key]; !ok || value != nil {
			t.Fatalf("%s must explicitly be null without a supported sample: %v", key, value)
		}
	}
}
