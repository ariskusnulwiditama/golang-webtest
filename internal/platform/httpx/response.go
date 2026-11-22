// Package httpx contains the transport helpers shared by every feature: the
// response envelope, request identifiers and the global middleware chain.
//
// The envelope is part of the public contract:
//
//	success: {"data": ..., "meta": {...}}      (meta only when paginated)
//	failure: {"error": {"code", "message", "details", "request_id"}}
package httpx

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
)

// Meta carries pagination information for list endpoints.
type Meta struct {
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

type envelope struct {
	Data any   `json:"data"`
	Meta *Meta `json:"meta,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// JSON writes a success envelope with an explicit status code.
func JSON(c *gin.Context, status int, data any) {
	c.JSON(status, envelope{Data: data})
}

// OK writes 200 with data.
func OK(c *gin.Context, data any) { JSON(c, http.StatusOK, data) }

// Created writes 201 with data.
func Created(c *gin.Context, data any) { JSON(c, http.StatusCreated, data) }

// NoContent writes 204 without a body.
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// Paginated writes 200 with data plus pagination metadata.
func Paginated(c *gin.Context, data any, meta Meta) {
	c.JSON(http.StatusOK, envelope{Data: data, Meta: &meta})
}

// Fail converts err into the error envelope and aborts the chain.
//
// Unknown errors become 500 with a generic message; the cause is logged, never
// serialised, so internal details cannot leak to clients.
func Fail(c *gin.Context, log *slog.Logger, err error) {
	appErr, ok := apperr.As(err)
	if !ok {
		appErr = apperr.Internal(err)
	}

	requestID := RequestIDFrom(c)
	attrs := []any{
		slog.String("request_id", requestID),
		slog.String("method", c.Request.Method),
		slog.String("path", c.Request.URL.Path),
		slog.Int("status", appErr.Status),
		slog.String("code", appErr.Code),
		slog.Any("error", appErr),
	}
	if appErr.Status >= http.StatusInternalServerError {
		log.Error("request failed", attrs...)
	} else {
		// Client mistakes are normal traffic; keep them out of the warn/error stream.
		log.Debug("request rejected", attrs...)
	}

	body := errorBody{
		Code:      appErr.Code,
		Message:   appErr.Message,
		Details:   appErr.Details,
		RequestID: requestID,
	}
	c.AbortWithStatusJSON(appErr.Status, errorEnvelope{Error: body})
}
