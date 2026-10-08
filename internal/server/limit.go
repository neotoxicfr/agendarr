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
}

const (
	pinBurst    = 10
	pinInterval = time.Second
)

func (l *limiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.tokens = pinBurst
	} else {
		l.tokens += float64(now.Sub(l.last)) / float64(pinInterval)
		if l.tokens > pinBurst {
			l.tokens = pinBurst
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
