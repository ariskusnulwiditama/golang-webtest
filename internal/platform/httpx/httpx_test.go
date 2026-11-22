package httpx_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newRouter(middleware ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware...)
	return r
}

func perform(r http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v (body=%q)", err, w.Body.String())
	}
	return body
}

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	r := newRouter(httpx.RequestID())
	r.GET("/x", func(c *gin.Context) { httpx.OK(c, gin.H{"id": httpx.RequestIDFrom(c)}) })

	w := perform(r, http.MethodGet, "/x", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	header := w.Header().Get(httpx.HeaderRequestID)
	if header == "" {
		t.Fatal("X-Request-ID header is missing from the response")
	}
	body := decode(t, w)
	data, _ := body["data"].(map[string]any)
	if data["id"] != header {
		t.Errorf("context request id = %v, header = %q; want them to match", data["id"], header)
	}
}

func TestRequestIDEchoesWellFormedValue(t *testing.T) {
	r := newRouter(httpx.RequestID())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := perform(r, http.MethodGet, "/x", map[string]string{httpx.HeaderRequestID: "abc-123.4"})
	if got := w.Header().Get(httpx.HeaderRequestID); got != "abc-123.4" {
		t.Errorf("X-Request-ID = %q, want abc-123.4", got)
	}
}

func TestRequestIDReplacesUnsafeValue(t *testing.T) {
	r := newRouter(httpx.RequestID())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	unsafe := "bad value\r\nX-Injected: yes"
	w := perform(r, http.MethodGet, "/x", map[string]string{httpx.HeaderRequestID: unsafe})

	if got := w.Header().Get(httpx.HeaderRequestID); got == unsafe || got == "" {
		t.Errorf("X-Request-ID = %q, want a freshly generated identifier", got)
	}
	if w.Header().Get("X-Injected") != "" {
		t.Error("unsafe header value was reflected")
	}
}

func TestFailWritesErrorEnvelope(t *testing.T) {
	log := testLogger()
	r := newRouter(httpx.RequestID())
	r.GET("/missing", func(c *gin.Context) { httpx.Fail(c, log, apperr.NotFound("project")) })

	w := perform(r, http.MethodGet, "/missing", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	body := decode(t, w)
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no error object: %v", body)
	}
	if errBody["code"] != apperr.CodeNotFound {
		t.Errorf("code = %v, want %s", errBody["code"], apperr.CodeNotFound)
	}
	if errBody["message"] != "project not found" {
		t.Errorf("message = %v, want \"project not found\"", errBody["message"])
	}
	if errBody["request_id"] == "" {
		t.Error("request_id is missing from the error envelope")
	}
	if _, hasData := body["data"]; hasData {
		t.Error("error responses must not include a data field")
	}
}

func TestFailHidesInternalCause(t *testing.T) {
	log := testLogger()
	r := newRouter(httpx.RequestID())
	r.GET("/boom", func(c *gin.Context) {
		httpx.Fail(c, log, errors.New("dial postgres://user:hunter2@db failed"))
	})

	w := perform(r, http.MethodGet, "/boom", nil)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Errorf("response leaked the internal cause: %s", w.Body.String())
	}
	body := decode(t, w)
	errBody, _ := body["error"].(map[string]any)
	if errBody["code"] != apperr.CodeInternal {
		t.Errorf("code = %v, want %s", errBody["code"], apperr.CodeInternal)
	}
}

func TestOKAndPaginatedEnvelopes(t *testing.T) {
	r := newRouter()
	r.GET("/one", func(c *gin.Context) { httpx.OK(c, gin.H{"id": "a"}) })
	r.GET("/many", func(c *gin.Context) {
		httpx.Paginated(c, []string{"a", "b"}, httpx.Meta{Page: 1, PerPage: 2, Total: 5, TotalPages: 3})
	})

	one := decode(t, perform(r, http.MethodGet, "/one", nil))
	if _, ok := one["data"]; !ok {
		t.Fatalf("data field is missing: %v", one)
	}
	if _, ok := one["meta"]; ok {
		t.Errorf("meta must be omitted for non-paginated responses: %v", one)
	}

	many := decode(t, perform(r, http.MethodGet, "/many", nil))
	meta, ok := many["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta is missing: %v", many)
	}
	if meta["total"] != float64(5) || meta["total_pages"] != float64(3) {
		t.Errorf("unexpected meta: %v", meta)
	}
}

func TestCORSAllowsConfiguredOriginOnly(t *testing.T) {
	r := newRouter(httpx.CORS([]string{"http://localhost:5173"}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	allowed := perform(r, http.MethodOptions, "/x", map[string]string{"Origin": "http://localhost:5173"})
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", allowed.Code)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the request origin", got)
	}

	blocked := perform(r, http.MethodGet, "/x", map[string]string{"Origin": "http://evil.example"})
	if got := blocked.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q for a foreign origin, want empty", got)
	}
}
