package traffic

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/go-gost/core/limiter"
)

func pendingEventually(t *testing.T, p *pacer, count int) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		p.mu.Lock()
		n := p.pending
		p.mu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending requests did not reach %d", count)
}

func TestShortBurstAndCancelledWaitDoNotGrantBytes(t *testing.T) {
	l := newLimiter(1000, 20*time.Millisecond, context.Background())
	defer l.Close()
	if n := l.Wait(context.Background(), 1000); n != 20 {
		t.Fatalf("initial burst = %d, want 20", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := l.Wait(ctx, 20); n != 0 {
		t.Fatalf("cancelled grant = %d", n)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if n := l.Wait(ctx, 20); n != 0 {
		t.Fatalf("expired grant = %d", n)
	}
}

func TestLowRateHasOneByteQuantumAndHotUpdateWakesWaiter(t *testing.T) {
	l := newLimiter(1, DefaultBurst, context.Background())
	defer l.Close()
	if n := l.Wait(context.Background(), 100); n != 1 {
		t.Fatalf("low-rate quantum = %d", n)
	}
	done := make(chan int, 1)
	go func() { done <- l.Wait(context.Background(), 1) }()
	pendingEventually(t, l.p, 1)
	l.Set(1000)
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("grant = %d", n)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("rate update did not wake waiter")
	}
}

func TestRateDecreaseResizesQueuedRequestWithoutRefillingTokens(t *testing.T) {
	l := newLimiter(1000, DefaultBurst, context.Background())
	defer l.Close()
	l.Wait(context.Background(), 100)
	done := make(chan int, 1)
	go func() { done <- l.Wait(context.Background(), 100) }()
	pendingEventually(t, l.p, 1)
	l.Set(10)
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("new quantum = %d", n)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("queued request exceeds new burst forever")
	}
	l.p.mu.Lock()
	l.p.tokens = 0
	l.p.last = time.Now()
	l.p.mu.Unlock()
	l.Set(1_000_000)
	l.p.mu.Lock()
	tokens := l.p.tokens
	l.p.mu.Unlock()
	if tokens > 1 {
		t.Fatalf("hot update refilled bucket: %f", tokens)
	}
}

func TestManyConnectionsDoNotMultiplyOneUsersShare(t *testing.T) {
	l := newLimiter(20_000, 25*time.Millisecond, context.Background())
	defer l.Close()
	l.Wait(context.Background(), 500)
	a, b := l.forGroup("user:1"), l.forGroup("user:2")
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	var one, two atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 33; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			lim, count := a, &one
			if i == 32 {
				lim, count = b, &two
			}
			for ctx.Err() == nil {
				n := lim.Wait(ctx, 500)
				if n <= 0 {
					return
				}
				count.Add(int64(n))
			}
		}(i)
	}
	workers.Wait()
	if two.Load() < 2500 || one.Load() > two.Load()+1500 {
		t.Fatalf("32 connections vs 1: %d vs %d bytes", one.Load(), two.Load())
	}
	pendingEventually(t, l.p, 0)
}

func TestFairSchedulingAccountsForBytesNotRequestCount(t *testing.T) {
	l := newLimiter(20_000, 25*time.Millisecond, context.Background())
	defer l.Close()
	l.Wait(context.Background(), 500)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var totals [2]atomic.Int64
	var workers sync.WaitGroup
	for i, size := range []int{500, 50} {
		workers.Add(1)
		go func(i, size int) {
			defer workers.Done()
			view := l.forGroup(string(rune('a' + i)))
			for ctx.Err() == nil {
				n := view.Wait(ctx, size)
				if n <= 0 {
					return
				}
				totals[i].Add(int64(n))
			}
		}(i, size)
	}
	workers.Wait()
	a, b := totals[0].Load(), totals[1].Load()
	if a < 2000 || b < 2000 || a-b > 1500 || b-a > 1500 {
		t.Fatalf("unequal byte shares: %d vs %d", a, b)
	}
}

func TestSharedAggregateRateEnvelopeAcrossUsers(t *testing.T) {
	l := newLimiter(20_000, 25*time.Millisecond, context.Background())
	defer l.Close()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var total atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			view := l.forGroup(string(rune('a' + i)))
			for ctx.Err() == nil {
				n := view.Wait(ctx, 500)
				if n <= 0 {
					return
				}
				total.Add(int64(n))
			}
		}(i)
	}
	workers.Wait()
	ceiling := int64(500 + 20_000*time.Since(start).Seconds())
	if total.Load() > ceiling+1 {
		t.Fatalf("aggregate cap expanded: %d > %d", total.Load(), ceiling)
	}
	if total.Load() < 4000 {
		t.Fatalf("unexpected underutilization: %d", total.Load())
	}
}

func TestPendingGroupsAreBoundedAndCloseReleasesWaiters(t *testing.T) {
	l := newLimiter(1, DefaultBurst, context.Background())
	l.Wait(context.Background(), 1)
	view := l.forGroup("user:1")
	var workers sync.WaitGroup
	for i := 0; i < maxGroupPending; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); view.Wait(context.Background(), 1) }()
	}
	pendingEventually(t, l.p, maxGroupPending)
	if n := view.Wait(context.Background(), 1); n != 0 {
		t.Fatalf("overfull group granted %d", n)
	}
	l.Close()
	workers.Wait()
	pendingEventually(t, l.p, 0)
	l.p.mu.Lock()
	groups := len(l.p.groups)
	l.p.mu.Unlock()
	if groups != 0 || l.Wait(context.Background(), 1) != 0 {
		t.Fatal("closed limiter retained requests or granted bytes")
	}
}

func TestOwnerContextCloseCancelsGrantsAndIdleRunnerExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := newLimiter(1, DefaultBurst, ctx)
	l.Wait(context.Background(), 1)
	done := make(chan int, 1)
	go func() { done <- l.Wait(context.Background(), 1) }()
	pendingEventually(t, l.p, 1)
	cancel()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("owner close did not cancel")
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		l.p.mu.Lock()
		running := l.p.running
		l.p.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runner survived owner cancellation")
}

func TestFluxViewsKeepAggregateIdentityAndFairnessKeys(t *testing.T) {
	l := NewTrafficLimiter(LimitsOption("$ 1MB 1MB"))
	defer l.(io.Closer).Close()
	a := l.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("1_7_9_tcp")).(*llimiter)
	b := l.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("2_8_9_udp")).(*llimiter)
	c := l.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("3_7_10_udp")).(*llimiter)
	if a.p != b.p || b.p != c.p || a.Limit() != 1024*1024 {
		t.Fatal("shared aggregate/units changed")
	}
	if a.group != c.group || a.group == b.group {
		t.Fatal("Flux user grouping failed")
	}
	if fairnessKey("none", "1_7_9_tcp") != "" || fairnessKey("user", "arbitrary_name") != "service:arbitrary_name" {
		t.Fatal("fairness compatibility failed")
	}
}

func TestConnectionReloadUsesNewRate(t *testing.T) {
	l := NewTrafficLimiter(LimitsOption("$$ 1000B 1000B")).(*trafficLimiter)
	defer l.Close()
	view := l.Out(context.Background(), "127.0.0.1:1234")
	l.options.limits = []string{"$$ 2000B 3000B"}
	if err := l.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view.Limit() != 3000 {
		t.Fatalf("retained previous rate %d", view.Limit())
	}
}

func TestGlobalPendingAndGroupCapsDoNotGrowUnderAdmissionPressure(t *testing.T) {
	for _, test := range []struct {
		name             string
		requests, groups int
	}{
		{"pending", maxPending, maxPending / maxGroupPending}, {"groups", maxGroups, maxGroups},
	} {
		t.Run(test.name, func(t *testing.T) {
			l := newLimiter(1, DefaultBurst, context.Background())
			defer l.Close()
			// Freeze refill while all requests are admitted; wall-clock scheduling
			// of thousands of callers must not affect this capacity assertion.
			l.p.mu.Lock()
			l.p.tokens = 0
			l.p.last = time.Now().Add(time.Hour)
			l.p.mu.Unlock()
			var workers sync.WaitGroup
			for i := 0; i < test.requests; i++ {
				workers.Add(1)
				go func(i int) { defer workers.Done(); l.forGroup(fmt.Sprint(i%test.groups)).Wait(context.Background(), 1) }(i)
			}
			pendingEventually(t, l.p, test.requests)
			if n := l.forGroup("extra-user").Wait(context.Background(), 1); n != 0 {
				t.Fatalf("over-cap request granted %d", n)
			}
			l.p.mu.Lock()
			requests, groups := l.p.pending, len(l.p.groups)
			l.p.mu.Unlock()
			if requests != test.requests || groups != test.groups {
				t.Fatalf("capacity grew: %d requests / %d groups", requests, groups)
			}
			l.Close()
			workers.Wait()
		})
	}
}
