package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
)

// fakeRepo is an in-memory Repository so the service rules are tested without a
// database. It mirrors the constraints that matter: unique email, not-found.
type fakeRepo struct {
	users     map[uuid.UUID]*User
	byEmail   map[string]uuid.UUID
	touchedAt time.Time
	createErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{users: map[uuid.UUID]*User{}, byEmail: map[string]uuid.UUID{}}
}

func (f *fakeRepo) Create(_ context.Context, user *User) error {
	if f.createErr != nil {
		return f.createErr
	}
	key := strings.ToLower(user.Email)
	if _, exists := f.byEmail[key]; exists {
		return ErrEmailTaken
	}
	stored := *user
	f.users[user.ID] = &stored
	f.byEmail[key] = user.ID
	return nil
}

func (f *fakeRepo) ByEmail(_ context.Context, email string) (*User, error) {
	id, ok := f.byEmail[strings.ToLower(email)]
	if !ok {
		return nil, ErrNotFound
	}
	return f.byID(id)
}

func (f *fakeRepo) ByID(_ context.Context, id uuid.UUID) (*User, error) {
	return f.byID(id)
}

func (f *fakeRepo) byID(id uuid.UUID) (*User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *user
	return &clone, nil
}

func (f *fakeRepo) UpdateProfile(_ context.Context, id uuid.UUID, name string) (*User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	user.Name = name
	return f.byID(id)
}

func (f *fakeRepo) UpdatePassword(_ context.Context, id uuid.UUID, hash string) (*User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	user.PasswordHash = hash
	return f.byID(id)
}

func (f *fakeRepo) TouchLogin(_ context.Context, id uuid.UUID, at time.Time) error {
	if _, ok := f.users[id]; !ok {
		return ErrNotFound
	}
	f.touchedAt = at
	return nil
}

func newTestService(repo Repository) *Service {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(repo, NewTestHasher(), testTokenizer(time.Hour), log)
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name   string
		in     RegisterInput
		fields []string
	}{
		{
			name:   "everything missing",
			in:     RegisterInput{},
			fields: []string{"email", "name", "password"},
		},
		{
			name:   "email without domain dot",
			in:     RegisterInput{Email: "qa@localhost", Name: "QA", Password: "long-enough-pass"},
			fields: []string{"email"},
		},
		{
			name:   "email with display name",
			in:     RegisterInput{Email: "QA <qa@example.com>", Name: "QA", Password: "long-enough-pass"},
			fields: []string{"email"},
		},
		{
			name:   "password too short",
			in:     RegisterInput{Email: "qa@example.com", Name: "QA", Password: "short"},
			fields: []string{"password"},
		},
		{
			name:   "password beyond bcrypt limit",
			in:     RegisterInput{Email: "qa@example.com", Name: "QA", Password: strings.Repeat("a", MaxPasswordBytes+1)},
			fields: []string{"password"},
		},
		{
			name:   "blank name",
			in:     RegisterInput{Email: "qa@example.com", Name: "   ", Password: "long-enough-pass"},
			fields: []string{"name"},
		},
		{
			name:   "name too long",
			in:     RegisterInput{Email: "qa@example.com", Name: strings.Repeat("a", MaxNameLength+1), Password: "long-enough-pass"},
			fields: []string{"name"},
		},
	}

	svc := newTestService(newFakeRepo())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Register(context.Background(), tc.in)
			appErr := requireAppError(t, err, 422)
			for _, field := range tc.fields {
				if _, ok := appErr.Details[field]; !ok {
					t.Errorf("details = %v, want key %q", appErr.Details, field)
				}
			}
		})
	}
}

func TestRegisterStoresNormalisedAccount(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	session, err := svc.Register(context.Background(), RegisterInput{
		Email:    "  QA@Example.COM ",
		Password: "long-enough-pass",
		Name:     "  Quality Bot  ",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got, want := session.User.Email, "qa@example.com"; got != want {
		t.Errorf("email = %q, want %q", got, want)
	}
	if got, want := session.User.Name, "Quality Bot"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if session.User.Role != RoleUser {
		t.Errorf("role = %q; self-registration must never grant admin", session.User.Role)
	}
	if !session.User.IsActive {
		t.Error("new accounts must be active")
	}
	if session.User.PasswordHash == "long-enough-pass" {
		t.Fatal("password stored in plaintext")
	}
	if !NewTestHasher().Verify(session.User.PasswordHash, "long-enough-pass") {
		t.Error("stored hash does not verify the password")
	}

	claims, err := svc.Tokenizer().Verify(session.Token.Value)
	if err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}
	if claims.Subject != session.User.ID.String() {
		t.Errorf("token subject = %q, want %q", claims.Subject, session.User.ID)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	in := RegisterInput{Email: "qa@example.com", Password: "long-enough-pass", Name: "QA"}

	if _, err := svc.Register(context.Background(), in); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err := svc.Register(context.Background(), in)
	requireAppError(t, err, 409)
}

func TestRegisterDatabaseFailureIsInternal(t *testing.T) {
	repo := newFakeRepo()
	repo.createErr = errors.New("connection reset")
	svc := newTestService(repo)

	_, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	})
	requireAppError(t, err, 500)
}

// requireAppError asserts err is an apperr with the given status and returns it.
func requireAppError(t *testing.T, err error, status int) *apperr.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with status %d, got nil", status)
	}
	appErr, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error %v is not an apperr", err)
	}
	if appErr.Status != status {
		t.Fatalf("status = %d, want %d (%v)", appErr.Status, status, err)
	}
	return appErr
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := NewTestHasher().Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return hash
}

func TestLoginRejectsBadCredentialsUniformly(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	if _, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	disabledID := uuid.New()
	repo.users[disabledID] = &User{
		ID:           disabledID,
		Email:        "off@example.com",
		PasswordHash: mustHash(t, "long-enough-pass"),
		Name:         "Disabled",
		Role:         RoleUser,
		IsActive:     false,
	}
	repo.byEmail["off@example.com"] = disabledID

	tests := []struct {
		name     string
		email    string
		password string
	}{
		{name: "unknown email", email: "nobody@example.com", password: "long-enough-pass"},
		{name: "wrong password", email: "qa@example.com", password: "wrong-password"},
		{name: "disabled account", email: "off@example.com", password: "long-enough-pass"},
		{name: "empty password", email: "qa@example.com", password: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session, err := svc.Login(context.Background(), tc.email, tc.password)
			if session != nil {
				t.Fatalf("Login returned a session for %s", tc.name)
			}
			appErr := requireAppError(t, err, 401)
			if appErr.Message != loginFailure {
				t.Errorf("message = %q; it must not reveal which part failed", appErr.Message)
			}
		})
	}
}

func TestLoginSucceedsAndRecordsTimestamp(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	if _, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	session, err := svc.Login(context.Background(), "  QA@EXAMPLE.com ", "long-enough-pass")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if session.User.Email != "qa@example.com" {
		t.Errorf("email = %q", session.User.Email)
	}
	if session.Token.Value == "" {
		t.Fatal("no token issued")
	}
	if _, err := svc.Tokenizer().Verify(session.Token.Value); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if repo.touchedAt.IsZero() {
		t.Error("last_login_at was not recorded")
	}
	if session.User.LastLoginAt == nil {
		t.Error("returned user does not show last_login_at")
	}
}

func TestGetRejectsMissingAndDisabledAccounts(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	if _, err := svc.Get(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected an error for an unknown account")
	} else {
		requireAppError(t, err, 401)
	}

	session, err := svc.Register(context.Background(), RegisterInput{
		Email: "off@example.com", Password: "long-enough-pass", Name: "Off",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	repo.users[session.User.ID].IsActive = false
	if _, err := svc.Get(context.Background(), session.User.ID); err == nil {
		t.Fatal("expected an error for a disabled account")
	} else {
		requireAppError(t, err, 401)
	}
}

func TestUpdateProfileNameOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	session, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	name := "  Renamed Bot  "
	updated, err := svc.UpdateProfile(context.Background(), session.User.ID, UpdateProfileInput{Name: &name})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.Name != "Renamed Bot" {
		t.Errorf("name = %q", updated.Name)
	}
	if updated.PasswordHash != session.User.PasswordHash {
		t.Error("password hash changed on a name-only update")
	}
	if updated.Email != session.User.Email {
		t.Error("email changed on a name-only update")
	}
}

func TestUpdateProfileValidation(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	session, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	str := func(v string) *string { return &v }

	tests := []struct {
		name  string
		in    UpdateProfileInput
		field string
	}{
		{name: "empty patch", in: UpdateProfileInput{}, field: "body"},
		{name: "blank name", in: UpdateProfileInput{Name: str("  ")}, field: "name"},
		{name: "missing current password", in: UpdateProfileInput{NewPassword: str("another-long-pass")}, field: "current_password"},
		{
			name:  "wrong current password",
			in:    UpdateProfileInput{CurrentPassword: str("not-the-password"), NewPassword: str("another-long-pass")},
			field: "current_password",
		},
		{
			name:  "same password",
			in:    UpdateProfileInput{CurrentPassword: str("long-enough-pass"), NewPassword: str("long-enough-pass")},
			field: "new_password",
		},
		{
			name:  "short new password",
			in:    UpdateProfileInput{CurrentPassword: str("long-enough-pass"), NewPassword: str("short")},
			field: "new_password",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateProfile(context.Background(), session.User.ID, tc.in)
			appErr := requireAppError(t, err, 422)
			if _, ok := appErr.Details[tc.field]; !ok {
				t.Errorf("details = %v, want key %q", appErr.Details, tc.field)
			}
		})
	}
}

func TestUpdateProfileChangesPassword(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)
	session, err := svc.Register(context.Background(), RegisterInput{
		Email: "qa@example.com", Password: "long-enough-pass", Name: "QA",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	oldPassword, newPassword := "long-enough-pass", "brand-new-long-pass"
	updated, err := svc.UpdateProfile(context.Background(), session.User.ID, UpdateProfileInput{
		CurrentPassword: &oldPassword,
		NewPassword:     &newPassword,
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if !NewTestHasher().Verify(updated.PasswordHash, newPassword) {
		t.Error("new password does not verify")
	}
	if NewTestHasher().Verify(updated.PasswordHash, oldPassword) {
		t.Error("old password still verifies after the change")
	}

	if _, err := svc.Login(context.Background(), "qa@example.com", oldPassword); err == nil {
		t.Error("login with the old password succeeded")
	}
	if _, err := svc.Login(context.Background(), "qa@example.com", newPassword); err != nil {
		t.Errorf("login with the new password failed: %v", err)
	}
}
