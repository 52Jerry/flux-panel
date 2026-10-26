package traffic

import (
	"context"
	"fmt"
	"sort"

	limiter "github.com/go-gost/core/limiter/traffic"
)

type limiterGroup struct {
	limiters []limiter.Limiter
}

func newLimiterGroup(limiters ...limiter.Limiter) *limiterGroup {
	sort.Slice(limiters, func(i, j int) bool { return limiters[i].Limit() < limiters[j].Limit() })
	return &limiterGroup{limiters: limiters}
}

func (l *limiterGroup) Wait(ctx context.Context, n int) int {
	// Charge the same byte count in every native bucket, even if a hot reload
	// changed which bucket now has the smaller pacing quantum.
	for _, lim := range l.limiters {
		if paced, ok := lim.(*llimiter); ok {
			paced.p.mu.Lock()
			n = min(n, paced.p.quantum(n))
			paced.p.mu.Unlock()
		}
	}
	for i := range l.limiters {
		if v := l.limiters[i].Wait(ctx, n); v < n {
			n = v
		}
		if n <= 0 {
			return 0
		}
	}
	return n
}

func (l *limiterGroup) Limit() int {
	n := 0
	for _, lim := range l.limiters {
		if v := lim.Limit(); v > 0 && (n == 0 || v < n) {
			n = v
		}
	}
	return n
}

func (l *limiterGroup) Set(n int) {}

func (l *limiterGroup) Closed() bool {
	for _, lim := range l.limiters {
		if c, ok := lim.(interface{ Closed() bool }); ok && c.Closed() {
			return true
		}
	}
	return false
}
func (l *limiterGroup) CacheIdentity() any { return l }

func (l *limiterGroup) String() string {
	return fmt.Sprintf("%v", l.limiters)
}
