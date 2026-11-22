// Package health serves the liveness and readiness endpoints used by humans,
// compose healthchecks and (later) an orchestrator.
//
// Liveness answers "is the process alive" and never touches dependencies;
// readiness answers "can this instance serve traffic" and checks them.
package health

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

// Checker is a dependency that can report whether it is usable.
type Checker interface {
	PingContext(ctx context.Context) error
}

// Handler serves GET /healthz and GET /readyz.
type Handler struct {
	version  string
	timeout  time.Duration
	checkers map[string]Checker
	log      *slog.Logger
}

// New builds the handler. Checkers are named so a failure identifies the dependency.
func New(log *slog.Logger, version string, timeout time.Duration, checkers map[string]Checker) *Handler {
	return &Handler{version: version, timeout: timeout, checkers: checkers, log: log}
}

// Register mounts the endpoints on the given router.
func (h *Handler) Register(r gin.IRouter) {
	r.GET("/healthz", h.Live)
	r.GET("/readyz", h.Ready)
}

// Live reports process liveness only; it must not fail because a dependency is down.
func (h *Handler) Live(c *gin.Context) {
	httpx.OK(c, gin.H{"status": "ok", "version": h.version})
}

// Ready pings every dependency with a bounded timeout.
func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	status := make(map[string]string, len(h.checkers))
	details := make(map[string]string)
	for name, checker := range h.checkers {
		if err := checker.PingContext(ctx); err != nil {
			status[name] = "unavailable"
			details[name] = "unavailable"
			continue
		}
		status[name] = "ok"
	}

	if len(details) > 0 {
		appErr := apperr.Unavailable("service not ready")
		appErr.Details = details
		httpx.Fail(c, h.log, appErr)
		return
	}

	httpx.OK(c, gin.H{"status": "ready", "version": h.version, "checks": status})
}
