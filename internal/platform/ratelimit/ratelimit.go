// Package ratelimit provides a small in-process token bucket used to slow down
// credential endpoints.
//
// It is deliberately process-local: v1 runs a single API replica (documented
// limitation), and a shared store would add a Redis dependency for two
// endpoints. Moving to a shared limiter later only changes this package.
package ratelimit

import (
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

const (
	// idleTTL is how long an untouched bucket survives before a sweep drops it,
	// so a scan with millions of source addresses cannot grow the map forever.
	idleTTL = 10 * time.Minute
	// sweepEvery throttles sweeping: at most once per minute per limiter.
	sweepEvery = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a per-key token bucket, safe for concurrent use.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	lastGC  time.Time
}

// New builds a limiter that allows burst requests at once and refills at
// perMinute tokens per minute.
func New(perMinute, burst int) *Limiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key and reports whether the call may proceed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RetryAfter is how long key must wait for its next token.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		return 0
	}
	missing := 1 - b.tokens
	if missing <= 0 {
		return 0
	}
	return time.Duration(missing / l.rate * float64(time.Second))
}

func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastGC) < sweepEvery {
		return
	}
	l.lastGC = now
	for key, b := range l.buckets {
		if now.Sub(b.last) > idleTTL {
			delete(l.buckets, key)
		}
	}
}

// Middleware rejects the request with the documented 429 envelope once the
// caller's bucket is empty, and advertises Retry-After.
func (l *Limiter) Middleware(log *slog.Logger, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := keyFn(c)
		if !l.Allow(key) {
			if wait := l.RetryAfter(key); wait > 0 {
				c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			}
			httpx.Fail(c, log, apperr.RateLimited("too many attempts, please try again later"))
			return
		}
		c.Next()
	}
}

// ByIP keys the bucket on the client address.
func ByIP(c *gin.Context) string { return c.ClientIP() }
