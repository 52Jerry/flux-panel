package limiter

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
	"github.com/go-gost/x/internal/util/cache"
)

const (
	defaultRefreshInterval = 30 * time.Second
	defaultCleanupInterval = 60 * time.Second
)

type options struct {
	refreshInterval time.Duration
	cleanupInterval time.Duration
	scope           string
}

type Option func(*options)

func RefreshIntervalOption(interval time.Duration) Option {
	return func(o *options) {
		o.refreshInterval = interval
	}
}

func CleanupIntervalOption(interval time.Duration) Option {
	return func(o *options) {
		o.cleanupInterval = interval
	}
}

func ScopeOption(scope string) Option {
	return func(o *options) {
		o.scope = scope
	}
}

type cachedTrafficLimiter struct {
	inLimits  *boundedCache
	outLimits *boundedCache
	limiter   traffic.TrafficLimiter
	options   options
}

func NewCachedTrafficLimiter(limiter traffic.TrafficLimiter, opts ...Option) traffic.TrafficLimiter {
	if limiter == nil {
		return nil
	}

	var options options
	for _, opt := range opts {
		opt(&options)
	}

	if options.refreshInterval == 0 {
		options.refreshInterval = defaultRefreshInterval
	}
	if options.refreshInterval < time.Second {
		options.refreshInterval = time.Second
	}

	if options.cleanupInterval == 0 {
		options.cleanupInterval = defaultCleanupInterval
	}
	if options.cleanupInterval < time.Second {
		options.cleanupInterval = time.Second
	}

	lim := &cachedTrafficLimiter{
		inLimits:  newBoundedCache(),
		outLimits: newBoundedCache(),
		limiter:   limiter,
		options:   options,
	}
	return lim
}

func (p *cachedTrafficLimiter) In(ctx context.Context, key string, opts ...limiter.Option) traffic.Limiter {
	if p.limiter == nil {
		return nil
	}

	var options limiter.Options
	for _, opt := range opts {
		opt(&options)
	}

	if p.options.scope != "" && p.options.scope != options.Scope {
		return nil
	}

	cacheKey := options.Scope + "\x00" + options.Service + "\x00" + key
	item := p.inLimits.Get(cacheKey)
	lim, _ := item.Value().(traffic.Limiter)
	if !item.Expired() && !isClosed(lim) {
		return lim
	}

	limNew := p.limiter.In(ctx, key, opts...)
	if limNew == nil && !isClosed(lim) {
		limNew = lim
	}
	if item == nil || !p.equal(lim, limNew) {
		p.inLimits.Set(cacheKey, cache.NewItem(limNew, p.options.refreshInterval))
		return limNew
	}

	p.inLimits.Set(cacheKey, cache.NewItem(lim, p.options.refreshInterval))

	return lim
}

func (p *cachedTrafficLimiter) Out(ctx context.Context, key string, opts ...limiter.Option) traffic.Limiter {
	if p.limiter == nil {
		return nil
	}

	var options limiter.Options
	for _, opt := range opts {
		opt(&options)
	}

	if p.options.scope != "" && p.options.scope != options.Scope {
		return nil
	}

	cacheKey := options.Scope + "\x00" + options.Service + "\x00" + key
	item := p.outLimits.Get(cacheKey)
	lim, _ := item.Value().(traffic.Limiter)
	if !item.Expired() && !isClosed(lim) {
		return lim
	}

	limNew := p.limiter.Out(ctx, key, opts...)
	if limNew == nil && !isClosed(lim) {
		limNew = lim
	}
	if item == nil || !p.equal(lim, limNew) {
		p.outLimits.Set(cacheKey, cache.NewItem(limNew, p.options.refreshInterval))
		return limNew
	}

	p.outLimits.Set(cacheKey, cache.NewItem(lim, p.options.refreshInterval))

	return lim
}

func (p *cachedTrafficLimiter) equal(lim1, lim2 traffic.Limiter) bool {
	if lim1 == lim2 {
		return true
	}

	if lim1 == nil || lim2 == nil {
		return false
	}
	if first, ok := lim1.(interface{ CacheIdentity() any }); ok {
		second, sameKind := lim2.(interface{ CacheIdentity() any })
		return sameKind && first.CacheIdentity() == second.CacheIdentity()
	}

	return lim1.Limit() == lim2.Limit()
}

func isClosed(lim traffic.Limiter) bool {
	closed, ok := lim.(interface{ Closed() bool })
	return ok && closed.Closed()
}

const maxCachedLimits = 4096

type boundedCache struct {
	mu     sync.Mutex
	items  map[string]*boundedEntry
	recent list.List
}
type boundedEntry struct {
	item     *cache.Item
	position *list.Element
}

func newBoundedCache() *boundedCache { return &boundedCache{items: make(map[string]*boundedEntry)} }
func (c *boundedCache) Get(key string) *cache.Item {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.items[key]; entry != nil {
		c.recent.MoveToFront(entry.position)
		return entry.item
	}
	return nil
}
func (c *boundedCache) Set(key string, item *cache.Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.items[key]; entry != nil {
		entry.item = item
		c.recent.MoveToFront(entry.position)
		return
	}
	if len(c.items) >= maxCachedLimits {
		oldest := c.recent.Back()
		delete(c.items, oldest.Value.(string))
		c.recent.Remove(oldest)
	}
	c.items[key] = &boundedEntry{item: item, position: c.recent.PushFront(key)}
}
