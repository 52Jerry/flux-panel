package traffic

import (
	"context"
	"errors"
	"testing"
	"time"

	core "github.com/go-gost/core/limiter"
)

func TestTryUpdateLimitsPreservesWaitingBucket(t *testing.T) {
	provider := NewTrafficLimiter(LimitsOption("$ 1B 1B")).(*trafficLimiter)
	defer provider.Close()
	lim := provider.Out(context.Background(), "$", core.ScopeOption(core.ScopeService)).(*llimiter)
	lim.Wait(context.Background(), 1)
	done := make(chan int, 1)
	go func() { done <- lim.Wait(context.Background(), 1) }()
	pendingEventually(t, lim.p, 1)
	rules := []string{"$ 10000B 10000B"}
	if ok, err := provider.TryUpdateLimits(rules); !ok || err != nil {
		t.Fatalf("update = %v %v", ok, err)
	}
	rules[0] = "$ 1B 1B" // caller must not be able to mutate future reloads.
	if err := provider.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lim.Limit() != 10000 {
		t.Fatalf("existing rate = %d", lim.Limit())
	}
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("waiting connection cancelled: %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("update did not wake waiter")
	}
}

func TestTryUpdateLimitsRejectsPolicyShapeChanges(t *testing.T) {
	provider := NewTrafficLimiter(LimitsOption("$ 100B 100B")).(*trafficLimiter)
	defer provider.Close()
	for _, rules := range [][]string{nil, {"$ 0 100B"}, {"$ 100B 0"}, {"$$ 100B 100B"}, {"$ 100B 100B", "$$ 1B 1B"}} {
		if ok, err := provider.TryUpdateLimits(rules); ok || err != nil {
			t.Fatalf("unsafe update %v = %v %v", rules, ok, err)
		}
	}
	lim := provider.Out(context.Background(), "$", core.ScopeOption(core.ScopeService))
	if lim == nil || lim.Limit() != 100 {
		t.Fatal("rejected update changed the policy")
	}
	provider.Close()
	if ok, err := provider.TryUpdateLimits([]string{"$ 200B 200B"}); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("closed provider update = %v %v", ok, err)
	}
}
