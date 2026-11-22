package apperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
)

func TestConstructorsMapToStatusAndCode(t *testing.T) {
	tests := []struct {
		name   string
		err    *apperr.Error
		status int
		code   string
	}{
		{"invalid", apperr.Invalid("malformed json"), http.StatusBadRequest, apperr.CodeInvalidInput},
		{"validation", apperr.Validation(map[string]string{"name": "required"}), http.StatusUnprocessableEntity, apperr.CodeValidation},
		{"unauthorized", apperr.Unauthorized("login required"), http.StatusUnauthorized, apperr.CodeUnauthorized},
		{"forbidden", apperr.Forbidden("not your project"), http.StatusForbidden, apperr.CodeForbidden},
		{"not found", apperr.NotFound("project"), http.StatusNotFound, apperr.CodeNotFound},
		{"method not allowed", apperr.MethodNotAllowed("POST", "/healthz"), http.StatusMethodNotAllowed, apperr.CodeMethodNotAllowed},
		{"conflict", apperr.Conflict("name already used"), http.StatusConflict, apperr.CodeConflict},
		{"rate limited", apperr.RateLimited("slow down"), http.StatusTooManyRequests, apperr.CodeRateLimited},
		{"internal", apperr.Internal(errors.New("boom")), http.StatusInternalServerError, apperr.CodeInternal},
		{"unavailable", apperr.Unavailable("database down"), http.StatusServiceUnavailable, apperr.CodeUnavailable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Status != tc.status {
				t.Errorf("Status = %d, want %d", tc.err.Status, tc.status)
			}
			if tc.err.Code != tc.code {
				t.Errorf("Code = %q, want %q", tc.err.Code, tc.code)
			}
			if tc.err.Message == "" {
				t.Error("Message must not be empty")
			}
		})
	}
}

func TestNotFoundBuildsReadableMessage(t *testing.T) {
	if got, want := apperr.NotFound("project").Message, "project not found"; got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
}

func TestInternalKeepsCauseOutOfMessage(t *testing.T) {
	cause := errors.New("connect postgres://user:hunter2@db")
	err := apperr.Internal(cause)

	if strings.Contains(err.Message, "hunter2") {
		t.Errorf("Message leaks the cause: %q", err.Message)
	}
	if !strings.Contains(err.Error(), "hunter2") {
		t.Error("Error() must include the cause for logs")
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is must reach the wrapped cause")
	}
}

func TestAsFindsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("service: %w", apperr.Conflict("already exists"))

	got, ok := apperr.As(wrapped)
	if !ok {
		t.Fatal("As did not recognise the wrapped *Error")
	}
	if got.Code != apperr.CodeConflict {
		t.Errorf("Code = %q, want %q", got.Code, apperr.CodeConflict)
	}

	if _, ok := apperr.As(errors.New("plain error")); ok {
		t.Error("As matched a plain error")
	}
	if _, ok := apperr.As(nil); ok {
		t.Error("As matched nil")
	}
}

func TestIsCode(t *testing.T) {
	err := fmt.Errorf("wrap: %w", apperr.NotFound("x"))

	if !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Error("IsCode should match the wrapped code")
	}
	if apperr.IsCode(err, apperr.CodeConflict) {
		t.Error("IsCode matched the wrong code")
	}
	if apperr.IsCode(errors.New("plain"), apperr.CodeNotFound) {
		t.Error("IsCode matched a plain error")
	}
}

func TestWithDetailAccumulates(t *testing.T) {
	err := apperr.Validation(nil).
		WithDetail("email", "is required").
		WithDetail("name", "is required")

	if len(err.Details) != 2 {
		t.Fatalf("Details has %d entries, want 2", len(err.Details))
	}
	if err.Details["email"] != "is required" {
		t.Errorf("Details[email] = %q", err.Details["email"])
	}
}
