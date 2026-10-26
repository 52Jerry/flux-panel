package registry

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/conn"
	"github.com/go-gost/core/limiter/rate"
	"github.com/go-gost/core/limiter/traffic"
)

type trafficLimiterRegistry struct {
	registry[traffic.TrafficLimiter]
	mu sync.Mutex
}

func (r *trafficLimiterRegistry) Register(name string, v traffic.TrafficLimiter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registry.Register(name, v)
}

func (r *trafficLimiterRegistry) Unregister(name string) {
	r.mu.Lock()
	v, _ := r.m.LoadAndDelete(name)
	r.mu.Unlock()
	if closer, ok := v.(io.Closer); ok {
		closer.Close()
	}
}

// ReplaceTrafficLimiter publishes the new policy before retiring the old one,
// so readers never observe an unregistered (unlimited) intermediate state.
func ReplaceTrafficLimiter(name string, replacement traffic.TrafficLimiter) error {
	r := trafficLimiterReg.(*trafficLimiterRegistry)
	r.mu.Lock()
	previous, found := r.m.Load(name)
	if !found {
		r.mu.Unlock()
		return errors.New("limiter " + name + " not found")
	}
	r.m.Store(name, replacement)
	r.mu.Unlock()
	if closer, ok := previous.(io.Closer); ok {
		closer.Close()
	}
	return nil
}

func (r *trafficLimiterRegistry) Get(name string) traffic.TrafficLimiter {
	if name != "" {
		return &trafficLimiterWrapper{name: name, r: r}
	}
	return nil
}

func (r *trafficLimiterRegistry) get(name string) traffic.TrafficLimiter {
	return r.registry.Get(name)
}

type trafficLimiterWrapper struct {
	name string
	r    *trafficLimiterRegistry
}

func (w *trafficLimiterWrapper) TryUpdateLimits(limits []string) (bool, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if updater, ok := w.r.get(w.name).(interface {
		TryUpdateLimits([]string) (bool, error)
	}); ok {
		return updater.TryUpdateLimits(limits)
	}
	return false, nil
}

func (w *trafficLimiterWrapper) In(ctx context.Context, key string, opts ...limiter.Option) traffic.Limiter {
	v := w.r.get(w.name)
	if v == nil {
		return nil
	}
	return v.In(ctx, key, opts...)
}

func (w *trafficLimiterWrapper) Out(ctx context.Context, key string, opts ...limiter.Option) traffic.Limiter {
	v := w.r.get(w.name)
	if v == nil {
		return nil
	}
	return v.Out(ctx, key, opts...)
}

type connLimiterRegistry struct {
	registry[conn.ConnLimiter]
}

func (r *connLimiterRegistry) Register(name string, v conn.ConnLimiter) error {
	return r.registry.Register(name, v)
}

func (r *connLimiterRegistry) Get(name string) conn.ConnLimiter {
	if name != "" {
		return &connLimiterWrapper{name: name, r: r}
	}
	return nil
}

func (r *connLimiterRegistry) get(name string) conn.ConnLimiter {
	return r.registry.Get(name)
}

type connLimiterWrapper struct {
	name string
	r    *connLimiterRegistry
}

func (w *connLimiterWrapper) Limiter(key string) conn.Limiter {
	v := w.r.get(w.name)
	if v == nil {
		return nil
	}
	return v.Limiter(key)
}

type rateLimiterRegistry struct {
	registry[rate.RateLimiter]
}

func (r *rateLimiterRegistry) Register(name string, v rate.RateLimiter) error {
	return r.registry.Register(name, v)
}

func (r *rateLimiterRegistry) Get(name string) rate.RateLimiter {
	if name != "" {
		return &rateLimiterWrapper{name: name, r: r}
	}
	return nil
}

func (r *rateLimiterRegistry) get(name string) rate.RateLimiter {
	return r.registry.Get(name)
}

type rateLimiterWrapper struct {
	name string
	r    *rateLimiterRegistry
}

func (w *rateLimiterWrapper) Limiter(key string) rate.Limiter {
	v := w.r.get(w.name)
	if v == nil {
		return nil
	}
	return v.Limiter(key)
}
