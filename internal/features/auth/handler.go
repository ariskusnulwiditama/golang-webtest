package auth

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

// Handler adapts the service to HTTP: it parses, delegates and shapes the
// response. Every rule lives in the service so it is testable without Gin.
type Handler struct {
	log *slog.Logger
	svc *Service
}

// NewHandler wires the transport layer.
func NewHandler(log *slog.Logger, svc *Service) *Handler {
	return &Handler{log: log, svc: svc}
}

// RegisterRoutes mounts the auth endpoints on /api/v1.
//
// The middleware arguments are passed in rather than built here so cmd/api owns
// the whole chain in one readable place.
func (h *Handler) RegisterRoutes(v1 *gin.RouterGroup, requireAuth, limitLogin, limitRegister gin.HandlerFunc) {
	group := v1.Group("/auth")
	group.POST("/register", limitRegister, h.register)
	group.POST("/login", limitLogin, h.login)
	group.GET("/me", requireAuth, h.me)
	group.PATCH("/me", requireAuth, h.updateMe)
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type updateProfileRequest struct {
	Name            *string `json:"name"`
	CurrentPassword *string `json:"current_password"`
	NewPassword     *string `json:"new_password"`
}

type sessionResponse struct {
	User      PublicUser `json:"user"`
	Token     string     `json:"token"`
	TokenType string     `json:"token_type"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type userResponse struct {
	User PublicUser `json:"user"`
}

func newSessionResponse(session *Session) sessionResponse {
	return sessionResponse{
		User:      session.User.Public(),
		Token:     session.Token.Value,
		TokenType: "Bearer",
		ExpiresAt: session.Token.ExpiresAt,
	}
}

func (h *Handler) register(c *gin.Context) {
	var req registerRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, h.log, err)
		return
	}

	session, err := h.svc.Register(c.Request.Context(), RegisterInput{
		Email:    req.Email,
		Password: req.Password,
		Name:     req.Name,
	})
	if err != nil {
		httpx.Fail(c, h.log, err)
		return
	}
	httpx.Created(c, newSessionResponse(session))
}

func (h *Handler) login(c *gin.Context) {
	var req loginRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, h.log, err)
		return
	}

	session, err := h.svc.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		httpx.Fail(c, h.log, err)
		return
	}
	httpx.OK(c, newSessionResponse(session))
}

func (h *Handler) me(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok {
		// Reachable only if the route is mounted without RequireAuth.
		httpx.Fail(c, h.log, apperr.Internal(errors.New("auth: /auth/me mounted without RequireAuth")))
		return
	}
	httpx.OK(c, userResponse{User: user.Public()})
}

func (h *Handler) updateMe(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok {
		httpx.Fail(c, h.log, apperr.Internal(errors.New("auth: /auth/me mounted without RequireAuth")))
		return
	}

	var req updateProfileRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, h.log, err)
		return
	}

	updated, err := h.svc.UpdateProfile(c.Request.Context(), user.ID, UpdateProfileInput{
		Name:            req.Name,
		CurrentPassword: req.CurrentPassword,
		NewPassword:     req.NewPassword,
	})
	if err != nil {
		httpx.Fail(c, h.log, err)
		return
	}
	httpx.OK(c, userResponse{User: updated.Public()})
}

// bindJSON turns a malformed body into a documented 400 instead of Gin's default
// plain-text 400, keeping the error envelope uniform.
func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		return apperr.Invalid("request body must be a JSON object").WithCause(err)
	}
	return nil
}
