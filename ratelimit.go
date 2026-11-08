package playhold

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit says how often something may happen: on average once every Every, with up to Burst at once. The zero value
// uses the layer's default; there is no way to remove a limit by leaving it out. Off turns the layer off, for a game
// behind a proxy that already limits it, and the server logs a warning at startup.
type RateLimit struct {
	Every time.Duration
	Burst int
	Off   bool
}

// The defaults, for turn-based games: far above what a player needs, and well below a flood.
var (
	defaultConnectRate = RateLimit{Every: 200 * time.Millisecond, Burst: 20} // new connections per IP
	defaultGuestRate   = RateLimit{Every: 6 * time.Second, Burst: 20}        // new guests per IP
	defaultMessageRate = RateLimit{Every: 50 * time.Millisecond, Burst: 40}  // frames per connection
)

// orDefault fills a zero field from d, unless the limit is off.
func (l RateLimit) orDefault(d RateLimit) RateLimit {
	if l.Off {
		return l
	}
	if l.Every <= 0 {
		l.Every = d.Every
	}
	if l.Burst <= 0 {
		l.Burst = d.Burst
	}
	return l
}

// limiter returns a limiter for one client, or nil if the limit is off.
func (l RateLimit) limiter() *rate.Limiter {
	if l.Off {
		return nil
	}
	return rate.NewLimiter(rate.Every(l.Every), l.Burst)
}

// allow reports whether one more is allowed now. A nil limiter allows everything.
func allow(l *rate.Limiter) bool { return l == nil || l.Allow() }

// sweepEvery is how often perIP forgets addresses that have been quiet long enough to be back at a full burst.
const sweepEvery = time.Minute

// perIP keeps one limiter per IP address.
type perIP struct {
	limit RateLimit

	mu        sync.Mutex
	limiters  map[string]*ipLimiter
	lastSweep time.Time
}

type ipLimiter struct {
	limiter *rate.Limiter
	seen    time.Time
}

// newPerIP returns per-IP limits, or nil if the limit is off.
func newPerIP(limit RateLimit) *perIP {
	if limit.Off {
		return nil
	}
	return &perIP{limit: limit, limiters: make(map[string]*ipLimiter), lastSweep: time.Now()}
}

// allow reports whether ip may do one more now. A nil perIP allows everything.
func (p *perIP) allow(ip string) bool {
	if p == nil {
		return true
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.lastSweep) >= sweepEvery {
		p.sweep(now)
	}
	entry := p.limiters[ip]
	if entry == nil {
		entry = &ipLimiter{limiter: p.limit.limiter()}
		p.limiters[ip] = entry
	}
	entry.seen = now
	return entry.limiter.AllowN(now, 1)
}

// sweep forgets every address quiet for long enough to have refilled its whole burst, which a new limiter starts with
// anyway, so forgetting it changes nothing for that address.
func (p *perIP) sweep(now time.Time) {
	refill := p.limit.Every * time.Duration(p.limit.Burst)
	for ip, entry := range p.limiters {
		if now.Sub(entry.seen) >= refill {
			delete(p.limiters, ip)
		}
	}
	p.lastSweep = now
}
