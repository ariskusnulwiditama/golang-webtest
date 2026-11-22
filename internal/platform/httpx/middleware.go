package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
)

const (
	// RequestIDKey is the gin context key holding the request identifier.
	RequestIDKey = "request_id"
	// HeaderRequestID is the header used to accept and echo a request identifier.
	HeaderRequestID = "X-Request-ID"
)

// RequestID accepts a well-formed inbound identifier or generates a new one, and
// echoes it back so a client error report can be traced through the logs.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		c.Set(RequestIDKey, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// RequestIDFrom returns the identifier set by RequestID, or "" when absent.
func RequestIDFrom(c *gin.Context) string {
	if v, ok := c.Get(RequestIDKey); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// validRequestID rejects values that could be used to inject headers or bloat logs.
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// AccessLog writes one structured line per request.
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.Request.URL.Path
		attrs := []any{
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", c.Writer.Status()),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes", c.Writer.Size()),
			slog.String("ip", c.ClientIP()),
			slog.String("request_id", RequestIDFrom(c)),
		}

		switch {
		case c.Writer.Status() >= http.StatusInternalServerError:
			log.Error("http request", attrs...)
		case isProbe(path):
			// Liveness/readiness probes are polled constantly; keep them out of
			// the normal log stream.
			log.Debug("http request", attrs...)
		default:
			log.Info("http request", attrs...)
		}
	}
}

func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// Recovery turns a panic into a logged 500 response instead of a dropped connection.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered",
					slog.String("request_id", RequestIDFrom(c)),
					slog.String("method", c.Request.Method),
					slog.String("path", c.Request.URL.Path),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				Fail(c, log, apperr.Internal(nil))
			}
		}()
		c.Next()
	}
}

// BodyLimit caps the accepted request body size.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// CORS allows the SPA origin(s) to call the API. The request origin is echoed
// (never "*") so credentialed requests keep working.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	wildcard := false
	for _, origin := range allowedOrigins {
		if origin == "*" {
			wildcard = true
		}
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			_, ok := allowed[origin]
			if wildcard || ok {
				h := c.Writer.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Expose-Headers", HeaderRequestID)
				h.Set("Access-Control-Max-Age", "600")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
