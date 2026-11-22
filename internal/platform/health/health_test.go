package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/health"
)

type stubChecker struct{ err error }

func (s stubChecker) PingContext(context.Context) error { return s.err }

func newRouter(checkers map[string]health.Checker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	r := gin.New()
	health.New(log, "test-version", time.Second, checkers).Register(r)
	return r
}

func get(r http.Handler, target string) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestLiveIgnoresDependencies(t *testing.T) {
	r := newRouter(map[string]health.Checker{"database": stubChecker{err: errors.New("db is down")}})

	w, body := get(r, "/healthz")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "ok" {
		t.Errorf("status = %v, want ok", data["status"])
	}
	if data["version"] != "test-version" {
		t.Errorf("version = %v, want test-version", data["version"])
	}
}

func TestReadyReportsHealthyDependencies(t *testing.T) {
	r := newRouter(map[string]health.Checker{"database": stubChecker{}})

	w, body := get(r, "/readyz")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "ready" {
		t.Errorf("status = %v, want ready", data["status"])
	}
	checks, _ := data["checks"].(map[string]any)
	if checks["database"] != "ok" {
		t.Errorf("checks.database = %v, want ok", checks["database"])
	}
}

func TestReadyFailsWhenDependencyIsDown(t *testing.T) {
	r := newRouter(map[string]health.Checker{"database": stubChecker{err: errors.New("connection refused")}})

	w, body := get(r, "/readyz")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no error object: %v", body)
	}
	if errBody["code"] != "service_unavailable" {
		t.Errorf("code = %v, want service_unavailable", errBody["code"])
	}
	details, _ := errBody["details"].(map[string]any)
	if details["database"] != "unavailable" {
		t.Errorf("details.database = %v, want unavailable", details["database"])
	}
	if strings.Contains(w.Body.String(), "connection refused") {
		t.Errorf("readiness response must not leak the dependency error: %s", w.Body.String())
	}
}
