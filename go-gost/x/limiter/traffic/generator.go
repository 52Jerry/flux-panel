package traffic

import (
	"context"
	limiter "github.com/go-gost/core/limiter/traffic"
	"time"
)

type limitGenerator struct {
	in    int
	out   int
	burst time.Duration
	ctx   context.Context
}

func newLimitGenerator(in, out int, burst time.Duration, ctx context.Context) *limitGenerator {
	return &limitGenerator{
		in:    in,
		out:   out,
		burst: burst,
		ctx:   ctx,
	}
}

func (p *limitGenerator) In() limiter.Limiter {
	if p == nil || p.in <= 0 {
		return nil
	}
	return newLimiter(p.in, p.burst, p.ctx)
}

func (p *limitGenerator) Out() limiter.Limiter {
	if p == nil || p.out <= 0 {
		return nil
	}
	return newLimiter(p.out, p.burst, p.ctx)
}
