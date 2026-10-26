package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	xstats "github.com/go-gost/x/observer/stats"
)

type unavailableObserver struct{}

func (unavailableObserver) Observe(context.Context, []observer.Event, ...observer.Option) error {
	return errors.New("observer offline")
}

func TestAccountingRetriesQuietTrafficAndPreservesConcurrentBytes(t *testing.T) {
	for _, withObserver := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-observer", true: "offline-observer"}[withObserver], func(t *testing.T) {
			st := xstats.NewStats(false)
			st.Add(stats.KindInputBytes, 100)
			var requests, accepted atomic.Int32
			done := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var report TrafficReportItem
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
				}
				switch requests.Add(1) {
				case 1:
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case 2:
					if report.D != 100 {
						t.Errorf("retry lost snapshot: %d", report.D)
					}
					st.Add(stats.KindInputBytes, 25)
				case 3:
					if report.D != 25 {
						t.Errorf("concurrent bytes lost or duplicated: %d", report.D)
					}
				}
				accepted.Add(int32(report.D))
				w.Write([]byte("ok"))
				if accepted.Load() == 125 {
					select {
					case done <- struct{}{}:
					default:
					}
				}
			}))
			defer server.Close()
			oldURL, oldCrypto := httpReportURL, httpAESCrypto
			httpReportURL, httpAESCrypto = server.URL, nil
			defer func() { httpReportURL, httpAESCrypto = oldURL, oldCrypto }()
			opts := []Option{StatsOption(st), ObserverPeriodOption(time.Second)}
			if withObserver {
				opts = append(opts, ObserverOption(unavailableObserver{}))
			}
			s, _, _ := newLifecycleService(t, opts...)
			ctx, cancel := context.WithCancel(context.Background())
			exited := make(chan struct{})
			go func() { defer close(exited); s.observeStats(ctx) }()
			defer func() { cancel(); <-exited }()
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatalf("accounting stopped: requests=%d accepted=%d", requests.Load(), accepted.Load())
			}
		})
	}
}
