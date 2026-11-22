package ratelimit

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newTestLimiter(perMinute, burst int) (*Limiter, *fakeClock) {
	clock := &fakeClock{at: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	l := New(perMinute, burst)
	l.now = clock.Now
	return l, clock
}

// fakeClock is a hand-wound clock so refill behaviour is tested without sleeps.
type fakeClock struct {
	at time.Time
}

func (c *fakeClock) Now() time.Time { return c.at }

func (c *fakeClock) Advance(d time.Duration) { c.at = c.at.Add(d) }

func TestAllowBurstThenRefill(t *testing.T) {
	l, clock := newTestLimiter(60, 3) // one token per second, burst 3

	for i := 1; i <= 3; i++ {
		if !l.Allow("ip-1") {
			t.Fatalf("request %d inside the burst was denied", i)
		}
	}
	if l.Allow("ip-1") {
		t.Fatal("request beyond the burst was allowed")
	}
	if !l.Allow("ip-2") {
		t.Error("a different key must have its own bucket")
	}

	clock.Advance(time.Second)
	if !l.Allow("ip-1") {
		t.Error("no token refilled after one second")
	}
	if l.Allow("ip-1") {
		t.Error("refill granted more than one token")
	}

	clock.Advance(2 * time.Second)
	allowed := 0
	for i := 0; i < 5; i++ {
		if l.Allow("ip-1") {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("allowed %d requests after a 2s refill, want 2", allowed)
	}
}

func TestRetryAfter(t *testing.T) {
	l, clock := newTestLimiter(60, 1)

	if wait := l.RetryAfter("ip"); wait != 0 {
		t.Errorf("unknown key wait = %s, want 0", wait)
	}
	l.Allow("ip")
	l.Allow("ip") // drains the bucket
	if wait := l.RetryAfter("ip"); wait != time.Second {
		t.Errorf("wait = %s, want 1s", wait)
	}

	clock.Advance(500 * time.Millisecond)
	l.Allow("ip") // still denied, but 0.5 tokens are back
	if wait := l.RetryAfter("ip"); wait != 500*time.Millisecond {
		t.Errorf("wait = %s, want 500ms", wait)
	}
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	l, clock := newTestLimiter(60, 1)

	l.Allow("stale")
	clock.Advance(11 * time.Minute)
	l.Allow("fresh")

	if _, ok := l.buckets["stale"]; ok {
		t.Error("idle bucket was not swept")
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Error("active bucket was swept")
	}
}

func TestMiddlewareReturns429Envelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l, _ := newTestLimiter(60, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	router := gin.New()
	router.GET("/limited", l.Middleware(log, ByIP), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/limited", nil)
		req.RemoteAddr = "203.0.113.7:4321"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}

	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"code":"rate_limited"`) {
		t.Errorf("body = %s, want the rate_limited envelope", body)
	}
}
