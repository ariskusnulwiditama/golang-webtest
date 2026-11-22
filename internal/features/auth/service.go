package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
)

// Account rules. They are deliberately a little stricter than the database
// constraints so users get a 422 with a readable message instead of a 500 from a
// column overflow.
const (
	// RoleUser is the only self-assignable role; "admin" is promoted out of band.
	RoleUser = "user"

	MinPasswordLength = 10
	// MaxPasswordBytes matches bcrypt's input limit: anything longer is silently
	// truncated by the algorithm, which would make two different passwords equal.
	MaxPasswordBytes = 72
	MaxNameLength    = 120
	MaxEmailLength   = 254
)

// loginFailure is the single message returned for unknown email, wrong password
// and disabled account, so login cannot be used to enumerate accounts.
const loginFailure = "invalid email or password"

// Service holds the account rules; it is the only place that hashes passwords or
// issues tokens.
type Service struct {
	repo   Repository
	hasher *Hasher
	tokens *Tokenizer
	log    *slog.Logger
	now    func() time.Time
}

// NewService wires the account service.
func NewService(repo Repository, hasher *Hasher, tokens *Tokenizer, log *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		hasher: hasher,
		tokens: tokens,
		log:    log,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Tokenizer exposes the token policy to the middleware.
func (s *Service) Tokenizer() *Tokenizer { return s.tokens }

// Session is a successful authentication: the user plus the token to send back.
type Session struct {
	User  *User
	Token Token
}

// RegisterInput is the payload of POST /auth/register.
type RegisterInput struct {
	Email    string
	Password string
	Name     string
}

// Register creates an account and logs it straight in.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*Session, error) {
	email, name, err := validateRegistration(in)
	if err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	user := &User{
		// The column has a default, but sending an explicit id keeps the insert
		// independent of RETURNING support in the driver.
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: hash,
		Name:         name,
		Role:         RoleUser,
		IsActive:     true,
	}
	if err := s.repo.Create(ctx, user); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return nil, apperr.Conflict("this email is already registered")
		}
		return nil, apperr.Internal(err)
	}

	token, err := s.tokens.Issue(user)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return &Session{User: user, Token: token}, nil
}

// Login verifies credentials and returns a fresh access token.
func (s *Service) Login(ctx context.Context, email, password string) (*Session, error) {
	email = normaliseEmail(email)
	if email == "" || password == "" {
		return nil, apperr.Unauthorized(loginFailure)
	}

	user, err := s.repo.ByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Spend the same CPU as a real comparison so the response time does
			// not reveal whether the address exists.
			s.hasher.CompareDummy(password)
			return nil, apperr.Unauthorized(loginFailure)
		}
		return nil, apperr.Internal(err)
	}

	if !s.hasher.Verify(user.PasswordHash, password) || !user.IsActive {
		return nil, apperr.Unauthorized(loginFailure)
	}

	token, err := s.tokens.Issue(user)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	if err := s.repo.TouchLogin(ctx, user.ID, s.now()); err != nil {
		// An audit timestamp is not worth failing a valid login over.
		s.log.Warn("could not record last_login_at", slog.Any("error", err))
	} else {
		lastLogin := s.now()
		user.LastLoginAt = &lastLogin
	}

	return &Session{User: user, Token: token}, nil
}

// Get returns an account by id, enforcing that it still exists and is active.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*User, error) {
	user, err := s.repo.ByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// The token is still valid but the account is gone: treat it as
			// unauthorized so a deleted user cannot keep using it.
			return nil, apperr.Unauthorized("account no longer exists")
		}
		return nil, apperr.Internal(err)
	}
	if !user.IsActive {
		return nil, apperr.Unauthorized("account is disabled")
	}
	return user, nil
}

// UpdateProfileInput is the payload of PATCH /auth/me. A nil pointer means
// "leave this field alone", which is what makes partial updates expressible.
type UpdateProfileInput struct {
	Name            *string
	CurrentPassword *string
	NewPassword     *string
}

// UpdateProfile changes the display name, the password, or both.
func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, in UpdateProfileInput) (*User, error) {
	if in.Name == nil && in.NewPassword == nil {
		return nil, apperr.Validation(map[string]string{"body": "provide name or new_password"})
	}

	details := map[string]string{}
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if msg := validateName(name); msg != "" {
			details["name"] = msg
		}
	}
	if in.NewPassword != nil {
		if in.CurrentPassword == nil || *in.CurrentPassword == "" {
			details["current_password"] = "is required to change the password"
		}
		if msg := validatePassword(*in.NewPassword); msg != "" {
			details["new_password"] = msg
		}
	}
	if len(details) > 0 {
		return nil, apperr.Validation(details)
	}

	user, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if in.NewPassword != nil {
		if !s.hasher.Verify(user.PasswordHash, *in.CurrentPassword) {
			return nil, apperr.Validation(map[string]string{"current_password": "is incorrect"})
		}
		if s.hasher.Verify(user.PasswordHash, *in.NewPassword) {
			return nil, apperr.Validation(map[string]string{"new_password": "must differ from the current password"})
		}
		hash, hashErr := s.hasher.Hash(*in.NewPassword)
		if hashErr != nil {
			return nil, apperr.Internal(hashErr)
		}
		if user, err = s.repo.UpdatePassword(ctx, id, hash); err != nil {
			return nil, persistErr(err)
		}
	}

	if in.Name != nil {
		if user, err = s.repo.UpdateProfile(ctx, id, name); err != nil {
			return nil, persistErr(err)
		}
	}

	return user, nil
}

// persistErr maps a write failure to the right HTTP status.
func persistErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apperr.Unauthorized("account no longer exists")
	}
	return apperr.Internal(err)
}

func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateRegistration normalises the input and collects every field problem in
// one 422 so the client can render all of them at once.
func validateRegistration(in RegisterInput) (email, name string, err error) {
	email = normaliseEmail(in.Email)
	name = strings.TrimSpace(in.Name)

	details := map[string]string{}
	if msg := validateEmail(email); msg != "" {
		details["email"] = msg
	}
	if msg := validateName(name); msg != "" {
		details["name"] = msg
	}
	if msg := validatePassword(in.Password); msg != "" {
		details["password"] = msg
	}
	if len(details) > 0 {
		return "", "", apperr.Validation(details)
	}
	return email, name, nil
}

func validateEmail(email string) string {
	switch {
	case email == "":
		return "is required"
	case len(email) > MaxEmailLength:
		return fmt.Sprintf("must be at most %d characters", MaxEmailLength)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "must be a valid email address"
	}
	domain := email[strings.LastIndexByte(email, '@')+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "must be a valid email address"
	}
	return ""
}

func validateName(name string) string {
	switch {
	case name == "":
		return "is required"
	case len([]rune(name)) > MaxNameLength:
		return fmt.Sprintf("must be at most %d characters", MaxNameLength)
	}
	return ""
}

func validatePassword(password string) string {
	switch {
	case password == "":
		return "is required"
	case len([]rune(password)) < MinPasswordLength:
		return fmt.Sprintf("must be at least %d characters", MinPasswordLength)
	case len(password) > MaxPasswordBytes:
		return fmt.Sprintf("must be at most %d bytes", MaxPasswordBytes)
	}
	return ""
}
