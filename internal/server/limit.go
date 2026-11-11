package server

import (
	"sync"
	"time"
)

// limiter is a token bucket: burst tokens, refilled at one per interval.
// Global rather than per IP: behind Traefik every client is the proxy, and
// what it protects (our plex.tv client id) is global too.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	// Zero values keep the pin defaults (pinBurst, pinInterval) (SEC-a2-02).
	burst float64
	every time.Duration
}

const (
	pinBurst    = 10
	pinInterval = time.Second
)

func (l *limiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	burst, every := l.burst, l.every
	if burst == 0 {
		burst = pinBurst
	}
	if every == 0 {
		every = pinInterval
	}
	if l.last.IsZero() {
		l.tokens = burst
	} else {
		l.tokens += float64(now.Sub(l.last)) / float64(every)
		if l.tokens > burst {
			l.tokens = burst
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// keyed holds one bucket per key (a pending sign-in, a username), so that one
// client draining its own does not lock everyone else out. When the map is
// full, buckets that have refilled go (a full bucket and a new one behave
// alike); if none has, a new key is refused: a flood of keys never hands a
// drained bucket a fresh burst.
type keyed struct {
	mu    sync.Mutex
	m     map[string]*limiter
	burst float64
	every time.Duration
}

const keyedMax = 1024

func (k *keyed) allow(key string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	l := k.m[key]
	if l == nil {
		if len(k.m) >= keyedMax {
			refilled := time.Duration(k.burst * float64(k.every))
			for old, b := range k.m {
				if now.Sub(b.last) >= refilled {
					delete(k.m, old)
				}
			}
			if len(k.m) >= keyedMax {
				return false
			}
		}
		if k.m == nil {
			k.m = map[string]*limiter{}
		}
		l = &limiter{burst: k.burst, every: k.every}
		k.m[key] = l
	}
	return l.allow(now)
}
