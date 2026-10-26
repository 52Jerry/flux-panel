// Package transportstats holds fixed-size process-wide forwarding counters.
// No client addresses, service labels, or connection objects are retained.
package transportstats

import (
	"sync/atomic"
	"time"
)

type Stats struct {
	ActiveTransfers          int64  `json:"active_transfers"`
	BytesForwardedTotal      uint64 `json:"bytes_forwarded_total"`
	WriteStallsTotal         uint64 `json:"write_stalls_total"`
	WriteTimeoutsTotal       uint64 `json:"write_timeouts_total"`
	ActiveStalledWrites      int64  `json:"active_stalled_writes"`
	MaxWriteBlockMillis      int64  `json:"max_write_block_millis"`
	WriteCallsTotal          uint64 `json:"write_calls_total"`
	WriteWaitNanosTotal      uint64 `json:"write_wait_nanos_total"`
	UnsupportedDeadlineTotal uint64 `json:"unsupported_deadline_total"`
}

var active atomic.Int64
var bytes atomic.Uint64
var stalls atomic.Uint64
var timeouts atomic.Uint64
var stalled atomic.Int64
var maxBlock atomic.Int64
var calls atomic.Uint64
var waitNanos atomic.Uint64
var unsupported atomic.Uint64

func TransferStarted()  { active.Add(1) }
func TransferFinished() { active.Add(-1) }
func BytesForwarded(n int) {
	if n > 0 {
		bytes.Add(uint64(n))
	}
}
func WriteStarted() { calls.Add(1) }
func WriteFinished(d time.Duration) {
	if d > 0 {
		waitNanos.Add(uint64(d))
	}
}
func StallStarted()        { stalls.Add(1); stalled.Add(1) }
func StallFinished()       { stalled.Add(-1) }
func WriteTimedOut()       { timeouts.Add(1) }
func DeadlineUnsupported() { unsupported.Add(1) }

// Active writes update at most one second apart; completed intervals update
// immediately. The value measures continuous lack of network write progress.
func ObserveBlock(d time.Duration) {
	ms := d.Milliseconds()
	for current := maxBlock.Load(); ms > current; current = maxBlock.Load() {
		if maxBlock.CompareAndSwap(current, ms) {
			return
		}
	}
}

func Snapshot() Stats {
	return Stats{
		ActiveTransfers: active.Load(), BytesForwardedTotal: bytes.Load(),
		WriteStallsTotal: stalls.Load(), WriteTimeoutsTotal: timeouts.Load(),
		ActiveStalledWrites: stalled.Load(), MaxWriteBlockMillis: maxBlock.Load(),
		WriteCallsTotal: calls.Load(), WriteWaitNanosTotal: waitNanos.Load(),
		UnsupportedDeadlineTotal: unsupported.Load(),
	}
}
