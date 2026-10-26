package traffic

import (
	"container/heap"
	"container/list"
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	limiter "github.com/go-gost/core/limiter/traffic"
)

const (
	DefaultBurst    = 100 * time.Millisecond
	maxBurstBytes   = 256 * 1024
	maxQuantum      = 32 * 1024
	maxPending      = 4096
	maxGroupPending = 128
	maxGroups       = 1024
)

// All views of a pacer consume the same aggregate byte budget.
type llimiter struct {
	p     *pacer
	group string
}

func NewLimiter(r int) limiter.Limiter { return newLimiter(r, DefaultBurst, context.Background()) }
func newLimiter(r int, burst time.Duration, ctx context.Context) *llimiter {
	if burst <= 0 {
		burst = DefaultBurst
	}
	if burst > time.Second {
		burst = time.Second
	}
	p := &pacer{ctx: ctx, rate: r, window: burst, last: time.Now(), wake: make(chan struct{}, 1), groups: make(map[string]*fairGroup)}
	p.tokens = float64(p.burst())
	return &llimiter{p: p}
}
func (l *llimiter) Wait(ctx context.Context, n int) int { return l.p.wait(ctx, l.group, n) }
func (l *llimiter) Limit() int                          { l.p.mu.Lock(); defer l.p.mu.Unlock(); return l.p.rate }
func (l *llimiter) Set(n int)                           { l.p.set(n) }
func (l *llimiter) String() string                      { return strconv.Itoa(l.Limit()) }
func (l *llimiter) Close() error                        { l.p.close(); return nil }
func (l *llimiter) Closed() bool {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	return l.p.closed || l.p.ctx.Err() != nil
}
func (l *llimiter) CacheIdentity() any {
	return struct {
		P     *pacer
		Group string
	}{l.p, l.group}
}
func (l *llimiter) forGroup(group string) limiter.Limiter { return &llimiter{p: l.p, group: group} }

type grantRequest struct {
	ctx     context.Context
	want, n int
	done    chan int
	entry   *list.Element
	group   *fairGroup
}
type fairGroup struct {
	key              string
	queue            list.List
	finish, priority float64
	index            int
}
type fairHeap []*fairGroup

func (h fairHeap) Len() int           { return len(h) }
func (h fairHeap) Less(i, j int) bool { return h[i].priority < h[j].priority }
func (h fairHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *fairHeap) Push(v any)        { g := v.(*fairGroup); g.index = len(*h); *h = append(*h, g) }
func (h *fairHeap) Pop() any {
	old := *h
	g := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	g.index = -1
	return g
}

// Only bounded grant requests are queued, never packets or socket writes.
// Empty groups are removed; an idle runner exits after 50 ms.
type pacer struct {
	mu              sync.Mutex
	ctx             context.Context
	rate            int
	window          time.Duration
	tokens          float64
	last            time.Time
	virtual         float64
	groups          map[string]*fairGroup
	ready           fairHeap
	pending         int
	running, closed bool
	wake            chan struct{}
}

func (p *pacer) burst() int {
	n := math.Ceil(float64(p.rate) * p.window.Seconds())
	if n < 1 {
		return 1
	}
	if n > maxBurstBytes {
		return maxBurstBytes
	}
	return int(n)
}
func (p *pacer) quantum(n int) int {
	if p.rate <= 0 {
		return n
	}
	return min(n, p.burst(), maxQuantum)
}
func (p *pacer) advance(now time.Time) {
	if now.After(p.last) {
		p.tokens = math.Min(float64(p.burst()), p.tokens+now.Sub(p.last).Seconds()*float64(max(0, p.rate)))
		p.last = now
	}
}
func (p *pacer) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *pacer) wait(ctx context.Context, key string, n int) int {
	if n <= 0 || ctx.Err() != nil || p.ctx.Err() != nil {
		return 0
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0
	}
	n = p.quantum(n)
	p.advance(time.Now())
	if len(p.ready) == 0 && (p.rate <= 0 || p.tokens >= float64(n)) {
		if p.rate > 0 {
			p.tokens -= float64(n)
		}
		p.mu.Unlock()
		return n
	}
	g := p.groups[key]
	perGroup := maxGroupPending
	if key == "" {
		perGroup = maxPending
	}
	if p.pending >= maxPending || (g != nil && g.queue.Len() >= perGroup) || (g == nil && len(p.groups) >= maxGroups) {
		p.mu.Unlock()
		return 0 // fail closed without an unbounded wait queue
	}
	if g == nil {
		g = &fairGroup{key: key, finish: p.virtual}
		p.groups[key] = g
	}
	r := &grantRequest{ctx: ctx, want: n, n: n, done: make(chan int, 1), group: g}
	r.entry = g.queue.PushBack(r)
	p.pending++
	if g.queue.Len() == 1 {
		g.priority = math.Max(g.finish, p.virtual) + float64(n)
		heap.Push(&p.ready, g)
	}
	if !p.running {
		p.running = true
		go p.run()
	}
	p.signal()
	p.mu.Unlock()
	select {
	case granted := <-r.done:
		return granted
	case <-ctx.Done():
		p.cancel(r)
		return 0
	case <-p.ctx.Done():
		p.cancel(r)
		return 0
	}
}
func (p *pacer) remove(r *grantRequest, granted bool) {
	if r.entry == nil {
		return
	}
	g := r.group
	head := g.queue.Front() == r.entry
	if granted {
		p.virtual = math.Max(p.virtual, g.priority)
		g.finish = g.priority
	}
	g.queue.Remove(r.entry)
	r.entry = nil
	p.pending--
	if g.queue.Len() == 0 {
		heap.Remove(&p.ready, g.index)
		delete(p.groups, g.key)
	} else if head {
		g.priority = math.Max(g.finish, p.virtual) + float64(g.queue.Front().Value.(*grantRequest).n)
		heap.Fix(&p.ready, g.index)
	}
}
func (p *pacer) cancel(r *grantRequest) { p.mu.Lock(); p.remove(r, false); p.signal(); p.mu.Unlock() }
func (p *pacer) run() {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		p.mu.Lock()
		if p.closed || p.ctx.Err() != nil {
			p.closeLocked()
			p.running = false
			p.mu.Unlock()
			return
		}
		if len(p.ready) == 0 {
			p.mu.Unlock()
			timer.Reset(50 * time.Millisecond)
			select {
			case <-p.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-p.ctx.Done():
			case <-timer.C:
				p.mu.Lock()
				if len(p.ready) == 0 {
					p.running = false
					p.mu.Unlock()
					return
				}
				p.mu.Unlock()
			}
			continue
		}
		r := p.ready[0].queue.Front().Value.(*grantRequest)
		if r.ctx.Err() != nil {
			p.remove(r, false)
			r.done <- 0
			p.mu.Unlock()
			continue
		}
		p.advance(time.Now())
		if p.rate <= 0 || p.tokens >= float64(r.n) {
			if p.rate > 0 {
				p.tokens -= float64(r.n)
			}
			p.remove(r, true)
			r.done <- r.n
			p.mu.Unlock()
			continue
		}
		delay := time.Duration(math.Ceil((float64(r.n) - p.tokens) / float64(p.rate) * float64(time.Second)))
		p.mu.Unlock()
		timer.Reset(max(delay, time.Microsecond))
		select {
		case <-p.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-p.ctx.Done():
		case <-timer.C:
		}
	}
}
func (p *pacer) set(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.advance(time.Now())
	p.rate = n
	p.tokens = math.Min(p.tokens, float64(p.burst()))
	for _, g := range p.groups {
		for e := g.queue.Front(); e != nil; e = e.Next() {
			r := e.Value.(*grantRequest)
			r.n = p.quantum(r.want)
		}
		g.priority = math.Max(g.finish, p.virtual) + float64(g.queue.Front().Value.(*grantRequest).n)
	}
	heap.Init(&p.ready)
	p.signal()
}
func (p *pacer) closeLocked() {
	p.closed = true
	for _, g := range p.groups {
		for e := g.queue.Front(); e != nil; e = e.Next() {
			r := e.Value.(*grantRequest)
			r.entry = nil
			r.done <- 0
		}
	}
	p.groups = make(map[string]*fairGroup)
	p.ready = nil
	p.pending = 0
}
func (p *pacer) close() {
	p.mu.Lock()
	if !p.closed {
		p.closeLocked()
	}
	p.signal()
	p.mu.Unlock()
}
