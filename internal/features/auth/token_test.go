package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// fixedNow is fixed for a test but anchored to the real clock, because jwt
// validation always compares against time.Now.
var fixedNow = time.Now().UTC().Truncate(time.Second)

func testTokenizer(ttl time.Duration) *Tokenizer {
	tk := NewTokenizer(config.Auth{
		JWTSecret:      testSecret,
		AccessTokenTTL: ttl,
		Issuer:         "webtest-studio-test",
	})
	tk.now = func() time.Time { return fixedNow }
	return tk
}

func testUser() *User {
	return &User{
		ID:    uuid.MustParse("6f1a3f2e-1c47-4f75-9d5e-0a4b1c2d3e4f"),
		Email: "qa@example.com",
		Role:  RoleUser,
	}
}

func TestTokenizerRoundTrip(t *testing.T) {
	tk := testTokenizer(time.Hour)
	user := testUser()

	token, err := tk.Issue(user)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got, want := token.ExpiresAt, fixedNow.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("ExpiresAt = %s, want %s", got, want)
	}

	claims, err := tk.Verify(token.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != user.ID.String() {
		t.Errorf("Subject = %q, want %q", claims.Subject, user.ID)
	}
	if claims.Role != RoleUser {
		t.Errorf("Role = %q, want %q", claims.Role, RoleUser)
	}
	if claims.Email != user.Email {
		t.Errorf("Email = %q, want %q", claims.Email, user.Email)
	}
	if claims.Issuer != "webtest-studio-test" {
		t.Errorf("Issuer = %q", claims.Issuer)
	}

	id, err := claims.UserID()
	if err != nil {
		t.Fatalf("UserID: %v", err)
	}
	if id != user.ID {
		t.Errorf("UserID = %s, want %s", id, user.ID)
	}
}

func TestTokenizerRejectsInvalidTokens(t *testing.T) {
	validClaims := func() Claims {
		return Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    "webtest-studio-test",
				Subject:   testUser().ID.String(),
				IssuedAt:  jwt.NewNumericDate(fixedNow),
				ExpiresAt: jwt.NewNumericDate(fixedNow.Add(time.Hour)),
			},
			Role: RoleUser,
		}
	}

	expired := testTokenizer(time.Hour)
	expired.now = func() time.Time { return fixedNow.Add(-3 * time.Hour) }
	expiredToken, err := expired.Issue(testUser())
	if err != nil {
		t.Fatalf("Issue expired: %v", err)
	}

	sign := func(method jwt.SigningMethod, key any, claims Claims) string {
		raw, signErr := jwt.NewWithClaims(method, claims).SignedString(key)
		if signErr != nil {
			t.Fatalf("sign test token: %v", signErr)
		}
		return raw
	}

	wrongIssuer := validClaims()
	wrongIssuer.Issuer = "someone-else"

	noSubject := validClaims()
	noSubject.Subject = ""

	tests := []struct {
		name string
		raw  string
	}{
		{name: "expired", raw: expiredToken.Value},
		{name: "signed with another secret", raw: sign(jwt.SigningMethodHS256, []byte("another-secret-another-secret-123"), validClaims())},
		{name: "wrong algorithm family", raw: sign(jwt.SigningMethodHS512, []byte(testSecret), validClaims())},
		{name: "unsigned", raw: sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims())},
		{name: "wrong issuer", raw: sign(jwt.SigningMethodHS256, []byte(testSecret), wrongIssuer)},
		{name: "missing subject", raw: sign(jwt.SigningMethodHS256, []byte(testSecret), noSubject)},
		{name: "garbage", raw: "not-a-jwt"},
		{name: "empty", raw: ""},
	}

	tk := testTokenizer(time.Hour)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := tk.Verify(tc.raw)
			if err == nil {
				t.Fatalf("Verify accepted the token: %+v", claims)
			}
			appErr, ok := apperr.As(err)
			if !ok {
				t.Fatalf("error %v is not an apperr", err)
			}
			if appErr.Code != apperr.CodeUnauthorized {
				t.Errorf("code = %q, want %q", appErr.Code, apperr.CodeUnauthorized)
			}
			if appErr.Message != "invalid or expired token" {
				t.Errorf("message = %q; failures must not reveal the reason", appErr.Message)
			}
		})
	}
}

func TestClaimsUserIDRejectsNonUUIDSubject(t *testing.T) {
	claims := &Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "42"}}
	if _, err := claims.UserID(); err == nil {
		t.Fatal("expected an error for a non-uuid subject")
	} else if appErr, ok := apperr.As(err); !ok || appErr.Status != 401 {
		t.Fatalf("error = %v, want a 401 apperr", err)
	}
}
