package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
)

// signingMethod is the only algorithm accepted on verification. Pinning it is
// what prevents the classic "alg: none" and algorithm-confusion attacks.
const signingMethod = "HS256"

// Claims is the payload of an access token.
type Claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Email string `json:"email"`
}

// Token is a signed access token and its expiry, so the client does not have to
// decode the JWT to know when to log out.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Tokenizer signs and verifies access tokens.
type Tokenizer struct {
	secret []byte
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenizer builds the tokenizer from validated configuration. Config
// validation guarantees the secret is at least 32 characters.
func NewTokenizer(cfg config.Auth) *Tokenizer {
	return &Tokenizer{
		secret: []byte(cfg.JWTSecret),
		issuer: cfg.Issuer,
		ttl:    cfg.AccessTokenTTL,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// TTL is how long a freshly issued token stays valid.
func (t *Tokenizer) TTL() time.Duration { return t.ttl }

// Issue signs an access token for the user.
func (t *Tokenizer) Issue(user *User) (Token, error) {
	issuedAt := t.now()
	expiresAt := issuedAt.Add(t.ttl)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    t.issuer,
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		Role:  user.Role,
		Email: user.Email,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return Token{}, fmt.Errorf("auth: sign access token: %w", err)
	}
	return Token{Value: signed, ExpiresAt: expiresAt}, nil
}

// Verify parses and validates a token.
//
// Every failure mode (bad signature, wrong algorithm, expired, wrong issuer,
// malformed subject) maps to the same 401 with the same message: the response
// must not tell an attacker which part of a forged token was wrong. The cause is
// attached for the log only.
func (t *Tokenizer) Verify(raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithIssuer(t.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, apperr.Unauthorized("invalid or expired token").WithCause(err)
	}

	subject, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, apperr.Unauthorized("invalid or expired token").WithCause(err)
	}
	claims.Subject = subject.String()
	return claims, nil
}

// UserID returns the authenticated user id from the claims.
func (c *Claims) UserID() (uuid.UUID, error) {
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil, apperr.Unauthorized("invalid or expired token").WithCause(err)
	}
	return id, nil
}
