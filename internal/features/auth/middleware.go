package auth

import (
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
)

// userContextKey is where RequireAuth stores the authenticated account.
const userContextKey = "auth.user"

// RequireAuth rejects requests without a valid bearer token, then loads the
// account so handlers never have to trust claims alone (a token outlives a
// delete or a deactivation otherwise).
func RequireAuth(log *slog.Logger, svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			httpx.Fail(c, log, apperr.Unauthorized("a bearer token is required"))
			return
		}

		claims, err := svc.Tokenizer().Verify(raw)
		if err != nil {
			httpx.Fail(c, log, err)
			return
		}
		id, err := claims.UserID()
		if err != nil {
			httpx.Fail(c, log, err)
			return
		}

		user, err := svc.Get(c.Request.Context(), id)
		if err != nil {
			httpx.Fail(c, log, err)
			return
		}

		c.Set(userContextKey, user)
		c.Next()
	}
}

// CurrentUser returns the account stored by RequireAuth. The boolean is false
// when the route was mounted without the middleware, which is a wiring bug.
func CurrentUser(c *gin.Context) (*User, bool) {
	value, ok := c.Get(userContextKey)
	if !ok {
		return nil, false
	}
	user, ok := value.(*User)
	return user, ok
}

// bearerToken parses "Bearer <token>" (scheme case-insensitive).
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
