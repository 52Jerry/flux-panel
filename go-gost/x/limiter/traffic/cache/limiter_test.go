package limiter

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	core "github.com/go-gost/core/limiter"
	traffic "github.com/go-gost/core/limiter/traffic"
	"github.com/go-gost/x/internal/util/cache"
	xtraffic "github.com/go-gost/x/limiter/traffic"
)

type replaceable struct{ lim traffic.Limiter }

func (p *replaceable) In(context.Context, string, ...core.Option) traffic.Limiter  { return p.lim }
func (p *replaceable) Out(context.Context, string, ...core.Option) traffic.Limiter { return p.lim }

func TestClosedLimiterRefreshesImmediatelyEvenAtSameRate(t *testing.T) {
	old := xtraffic.NewLimiter(1000)
	provider := &replaceable{lim: old}
	cached := NewCachedTrafficLimiter(provider)
	if cached.Out(context.Background(), "$") != old {
		t.Fatal("initial limiter missing")
	}
	old.(io.Closer).Close()
	replacement := xtraffic.NewLimiter(1000)
	defer replacement.(io.Closer).Close()
	provider.lim = replacement
	if cached.Out(context.Background(), "$") != replacement {
		t.Fatal("same-rate replacement retained closed generation")
	}
	replacement.(io.Closer).Close()
	provider.lim = nil
	if cached.Out(context.Background(), "$") != nil {
		t.Fatal("deleted rule retained closed limiter")
	}
}

func TestServiceIdentityIsPartOfCacheKey(t *testing.T) {
	provider := xtraffic.NewTrafficLimiter(xtraffic.LimitsOption("$ 1MB 1MB"))
	defer provider.(io.Closer).Close()
	cached := NewCachedTrafficLimiter(provider)
	a := cached.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("1_2_3_tcp"))
	b := cached.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("4_5_6_tcp"))
	if a == b {
		t.Fatal("different user views were merged by cache")
	}
}

func TestCacheHasHardBoundUnderHistoricalKeyChurn(t *testing.T) {
	lim := xtraffic.NewLimiter(1000)
	defer lim.(io.Closer).Close()
	cached := NewCachedTrafficLimiter(&replaceable{lim: lim}).(*cachedTrafficLimiter)
	for i := 0; i < maxCachedLimits+100; i++ {
		cached.In(context.Background(), fmt.Sprint(i))
	}
	if len(cached.inLimits.items) != maxCachedLimits {
		t.Fatalf("cache grew to %d", len(cached.inLimits.items))
	}
}

func TestCacheEvictsLeastRecentlyUsedInConstantSpace(t *testing.T) {
	c := newBoundedCache()
	for i := 0; i < maxCachedLimits; i++ {
		c.Set(fmt.Sprint(i), cache.NewItem(i, time.Minute))
	}
	c.Get("0")
	c.Set("new", cache.NewItem("new", time.Minute))
	if c.Get("0") == nil || c.Get("1") != nil || c.Get("new") == nil {
		t.Fatal("LRU eviction order changed")
	}
	if c.recent.Len() != maxCachedLimits || len(c.items) != maxCachedLimits {
		t.Fatal("cache/index grew past capacity")
	}
}
