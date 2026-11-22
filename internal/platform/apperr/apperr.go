// Package apperr defines the typed application errors that the transport layer
// maps to a stable response envelope.
//
// Services return these errors; handlers never build ad-hoc error strings. The
// code strings are part of the public API contract and are used by the SPA to
// branch on failures, so they must stay stable.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Stable error codes (see docs/ARCHITECTURE.md §10).
const (
	CodeInvalidInput = "invalid_input"
	CodeValidation   = "validation_failed"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	// CodeMethodNotAllowed reports a known route called with the wrong verb.
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
)

// Error is an application error with a stable code, an HTTP status and details
// that are safe to return to the client.
type Error struct {
	Code    string
	Message string
	// Details maps a field name to a human-readable problem, used by form
	// validation. Never contains internal identifiers or stack traces.
	Details map[string]string
	Status  int

	cause error
}

// New creates an error. Prefer the named constructors below.
func New(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

// Error implements the error interface. The cause is included because it is only
// ever written to logs, never to the response body.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.cause }

// WithDetail records a field-level detail on e and returns e for chaining.
func (e *Error) WithDetail(field, message string) *Error {
	if e.Details == nil {
		e.Details = make(map[string]string, 1)
	}
	e.Details[field] = message
	return e
}

// WithCause returns a copy carrying the underlying error for logging.
func (e *Error) WithCause(cause error) *Error {
	clone := *e
	clone.cause = cause
	return &clone
}

// Invalid reports malformed input: unparsable JSON, bad path parameter, ...
func Invalid(message string) *Error {
	return New(http.StatusBadRequest, CodeInvalidInput, message)
}

// Validation reports a payload that parsed but broke the rules.
func Validation(details map[string]string) *Error {
	return &Error{
		Code:    CodeValidation,
		Message: "validation failed",
		Details: details,
		Status:  http.StatusUnprocessableEntity,
	}
}

// Unauthorized reports missing or invalid credentials.
func Unauthorized(message string) *Error {
	return New(http.StatusUnauthorized, CodeUnauthorized, message)
}

// Forbidden reports an authenticated user lacking permission.
func Forbidden(message string) *Error {
	return New(http.StatusForbidden, CodeForbidden, message)
}

// NotFound reports a missing resource. Foreign resources are also reported as
// not found so that identifiers cannot be probed (see ARCHITECTURE §10).
func NotFound(resource string) *Error {
	return New(http.StatusNotFound, CodeNotFound, resource+" not found")
}

// MethodNotAllowed reports a known route called with an unsupported HTTP verb.
func MethodNotAllowed(method, path string) *Error {
	return New(http.StatusMethodNotAllowed, CodeMethodNotAllowed, method+" is not allowed on "+path)
}

// Conflict reports a uniqueness or state conflict.
func Conflict(message string) *Error {
	return New(http.StatusConflict, CodeConflict, message)
}

// RateLimited reports an exceeded quota.
func RateLimited(message string) *Error {
	return New(http.StatusTooManyRequests, CodeRateLimited, message)
}

// Internal wraps an unexpected failure. The cause is logged, never returned.
func Internal(cause error) *Error {
	return (&Error{
		Code:    CodeInternal,
		Message: "internal server error",
		Status:  http.StatusInternalServerError,
	}).WithCause(cause)
}

// Unavailable reports a dependency the request cannot do without.
func Unavailable(message string) *Error {
	return New(http.StatusServiceUnavailable, CodeUnavailable, message)
}

// As extracts the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}

// IsCode reports whether err carries the given application code.
func IsCode(err error, code string) bool {
	if appErr, ok := As(err); ok {
		return appErr.Code == code
	}
	return false
}
