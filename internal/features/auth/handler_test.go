package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

// newTestEngine mounts the auth routes the same way cmd/api does, minus the
// global middleware (which has its own tests) and with pass-through limiters.
func newTestEngine(t *testing.T) (*gin.Engine, *fakeRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	repo := newFakeRepo()
	svc := newTestService(repo)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	router := gin.New()
	router.Use(httpx.RequestID())
	NewHandler(log, svc).RegisterRoutes(
		router.Group("/api/v1"),
		RequireAuth(log, svc),
		passThrough(),
		passThrough(),
	)
	return router, repo
}

func passThrough() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Details   map[string]string `json:"details"`
		RequestID string            `json:"request_id"`
	} `json:"error"`
}

type sessionPayload struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	} `json:"user"`
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

type userPayload struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	} `json:"user"`
}

func request(t *testing.T, router *gin.Engine, method, path, body, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var env envelope
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s %s returned invalid JSON: %v (%s)", method, path, err, rec.Body.String())
		}
	}
	return rec, env
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode data: %v (%s)", err, raw)
	}
	return value
}

func TestHandlerRegisterMePatchLogin(t *testing.T) {
	router, _ := newTestEngine(t)

	// Register logs the account in immediately.
	rec, env := request(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"QA@Example.com","password":"long-enough-pass","name":"QA Bot"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	session := decode[sessionPayload](t, env.Data)
	if session.Token == "" {
		t.Fatal("register response has no token")
	}
	if session.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", session.TokenType)
	}
	if session.User.Email != "qa@example.com" {
		t.Errorf("email = %q, want the normalised address", session.User.Email)
	}
	if session.ExpiresAt.IsZero() {
		t.Error("expires_at is zero")
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response leaks password material: %s", rec.Body.String())
	}

	// Unauthenticated /me is a 401 envelope correlated by request id.
	rec, env = request(t, router, http.MethodGet, "/api/v1/auth/me", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without token = %d, want 401", rec.Code)
	}
	if env.Error == nil || env.Error.Code != apperr.CodeUnauthorized {
		t.Fatalf("error = %+v, want code %q", env.Error, apperr.CodeUnauthorized)
	}
	if env.Error.RequestID == "" || rec.Header().Get("X-Request-ID") != env.Error.RequestID {
		t.Errorf("request ids do not correlate: header %q, body %q",
			rec.Header().Get("X-Request-ID"), env.Error.RequestID)
	}

	// A garbage token is rejected the same way.
	if rec, _ = request(t, router, http.MethodGet, "/api/v1/auth/me", "", "not-a-jwt"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me with a bad token = %d, want 401", rec.Code)
	}

	// The real token works.
	rec, env = request(t, router, http.MethodGet, "/api/v1/auth/me", "", session.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	me := decode[userPayload](t, env.Data)
	if me.User.ID != session.User.ID {
		t.Errorf("me id = %q, want %q", me.User.ID, session.User.ID)
	}

	// Partial update changes only the name.
	rec, env = request(t, router, http.MethodPatch, "/api/v1/auth/me", `{"name":"Renamed Bot"}`, session.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch me = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if patched := decode[userPayload](t, env.Data); patched.User.Name != "Renamed Bot" {
		t.Errorf("name = %q, want Renamed Bot", patched.User.Name)
	}

	// Login returns a fresh token.
	rec, env = request(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"email":"qa@example.com","password":"long-enough-pass"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if decode[sessionPayload](t, env.Data).Token == "" {
		t.Fatal("login response has no token")
	}
}

func TestHandlerValidationEnvelope(t *testing.T) {
	router, _ := newTestEngine(t)

	rec, env := request(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"not-an-email","password":"short","name":""}`, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("missing error envelope")
	}
	if env.Error.Code != apperr.CodeValidation {
		t.Errorf("code = %q, want %q", env.Error.Code, apperr.CodeValidation)
	}
	for _, field := range []string{"email", "name", "password"} {
		if _, ok := env.Error.Details[field]; !ok {
			t.Errorf("details = %v, want key %q", env.Error.Details, field)
		}
	}
	if env.Error.RequestID == "" {
		t.Error("validation error has no request_id")
	}
}

func TestHandlerMalformedJSON(t *testing.T) {
	router, _ := newTestEngine(t)

	tests := []struct {
		name string
		body string
	}{
		{name: "truncated", body: `{"email":`},
		{name: "not an object", body: `[]`},
		{name: "empty", body: ``},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, env := request(t, router, http.MethodPost, "/api/v1/auth/login", tc.body, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			if env.Error == nil || env.Error.Code != apperr.CodeInvalidInput {
				t.Fatalf("error = %+v, want code %q", env.Error, apperr.CodeInvalidInput)
			}
		})
	}
}

func TestHandlerLoginFailuresAreIndistinguishable(t *testing.T) {
	router, _ := newTestEngine(t)
	if rec, _ := request(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"qa@example.com","password":"long-enough-pass","name":"QA"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d", rec.Code)
	}

	_, wrongPassword := request(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"email":"qa@example.com","password":"wrong-password"}`, "")
	_, unknownEmail := request(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"wrong-password"}`, "")

	if wrongPassword.Error == nil || unknownEmail.Error == nil {
		t.Fatal("expected error envelopes for both failures")
	}
	if wrongPassword.Error.Message != unknownEmail.Error.Message {
		t.Errorf("messages differ: %q vs %q", wrongPassword.Error.Message, unknownEmail.Error.Message)
	}
	if wrongPassword.Error.Code != unknownEmail.Error.Code {
		t.Errorf("codes differ: %q vs %q", wrongPassword.Error.Code, unknownEmail.Error.Code)
	}
}

func TestHandlerPatchPassword(t *testing.T) {
	router, _ := newTestEngine(t)

	rec, env := request(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"qa@example.com","password":"long-enough-pass","name":"QA"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
	}
	token := decode[sessionPayload](t, env.Data).Token

	rec, _ = request(t, router, http.MethodPatch, "/api/v1/auth/me",
		`{"current_password":"long-enough-pass","new_password":"brand-new-long-pass"}`, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch password = %d (%s)", rec.Code, rec.Body.String())
	}

	if rec, _ = request(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"email":"qa@example.com","password":"long-enough-pass"}`, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", rec.Code)
	}
	if rec, _ = request(t, router, http.MethodPost, "/api/v1/auth/login",
		`{"email":"qa@example.com","password":"brand-new-long-pass"}`, ""); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200", rec.Code)
	}
}

func TestHandlerDuplicateRegisterIsConflict(t *testing.T) {
	router, _ := newTestEngine(t)
	body := `{"email":"qa@example.com","password":"long-enough-pass","name":"QA"}`

	if rec, _ := request(t, router, http.MethodPost, "/api/v1/auth/register", body, ""); rec.Code != http.StatusCreated {
		t.Fatalf("first register = %d", rec.Code)
	}
	rec, env := request(t, router, http.MethodPost, "/api/v1/auth/register", body, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate register = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if env.Error == nil || env.Error.Code != apperr.CodeConflict {
		t.Fatalf("error = %+v, want code %q", env.Error, apperr.CodeConflict)
	}
}

func TestHandlerPatchRejectsUnauthenticated(t *testing.T) {
	router, _ := newTestEngine(t)
	rec, env := request(t, router, http.MethodPatch, "/api/v1/auth/me", `{"name":"x"}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if env.Error == nil || env.Error.Code != apperr.CodeUnauthorized {
		t.Fatalf("error = %+v", env.Error)
	}
}
