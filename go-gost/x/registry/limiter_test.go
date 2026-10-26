package registry

import (
	"context"
	"testing"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
)

type replacementTestLimiter struct{ onClose func() }

func (*replacementTestLimiter) In(context.Context, string, ...limiter.Option) traffic.Limiter {
	return nil
}
func (*replacementTestLimiter) Out(context.Context, string, ...limiter.Option) traffic.Limiter {
	return nil
}
func (l *replacementTestLimiter) Close() error {
	if l.onClose != nil {
		l.onClose()
	}
	return nil
}

func TestReplaceTrafficLimiterPublishesBeforeClosingOld(t *testing.T) {
	const name = "test-atomic-limiter-replace"
	r := trafficLimiterReg.(*trafficLimiterRegistry)
	replacement := &replacementTestLimiter{}
	closed := false
	old := &replacementTestLimiter{onClose: func() {
		closed = true
		if r.get(name) != replacement {
			t.Error("old Close observed a missing or stale registry entry")
		}
	}}
	if err := r.Register(name, old); err != nil {
		t.Fatal(err)
	}
	defer r.Unregister(name)
	if err := ReplaceTrafficLimiter(name, replacement); err != nil {
		t.Fatal(err)
	}
	if !closed || r.get(name) != replacement {
		t.Fatal("replacement was not published and old limiter closed")
	}
}
