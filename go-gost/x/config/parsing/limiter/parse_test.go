package limiter

import (
	"context"
	"io"
	"testing"

	core "github.com/go-gost/core/limiter"
	"github.com/go-gost/x/config"
)

func TestPacingMetadataAndAggregateFairnessCompatibility(t *testing.T) {
	l := ParseTrafficLimiter(&config.LimiterConfig{Name: "test", Limits: []string{"$ 1000B 1000B"},
		Metadata: map[string]any{"traffic.burst": "20ms", "traffic.fairness": "none"}})
	defer l.(io.Closer).Close()
	a := l.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("1_2_3_tcp"))
	b := l.Out(context.Background(), "$", core.ScopeOption(core.ScopeService), core.ServiceOption("4_5_6_udp"))
	if a.Wait(context.Background(), 1000) != 20 {
		t.Fatal("burst metadata ignored")
	}
	identity := func(v any) any { return v.(interface{ CacheIdentity() any }).CacheIdentity() }
	if identity(a) != identity(b) {
		t.Fatal("none mode duplicated shared aggregate")
	}
}
