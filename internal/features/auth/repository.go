package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Repository outcomes the service maps to HTTP statuses.
var (
	// ErrNotFound means no row matched; callers decide whether that is a 401
	// (login) or a 404 (profile lookup).
	ErrNotFound = errors.New("auth: user not found")
	// ErrEmailTaken means the users_email_key unique constraint fired.
	ErrEmailTaken = errors.New("auth: email already registered")
)

// uniqueViolation is the PostgreSQL SQLSTATE for a unique constraint breach.
const uniqueViolation = "23505"

// Repository is the persistence seam for accounts. The service depends on this
// interface so its rules are testable without a database.
type Repository interface {
	Create(ctx context.Context, user *User) error
	ByEmail(ctx context.Context, email string) (*User, error)
	ByID(ctx context.Context, id uuid.UUID) (*User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, name string) (*User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string) (*User, error)
	TouchLogin(ctx context.Context, id uuid.UUID, at time.Time) error
}

// NewRepository returns the GORM-backed repository.
func NewRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

type gormRepository struct{ db *gorm.DB }

func (r *gormRepository) Create(ctx context.Context, user *User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrEmailTaken
		}
		return fmt.Errorf("auth: create user: %w", err)
	}
	return nil
}

func (r *gormRepository) ByEmail(ctx context.Context, email string) (*User, error) {
	// The column is citext, so this comparison is case-insensitive in SQL even
	// though the caller already normalised the address.
	return r.first(ctx, "email = ?", email)
}

func (r *gormRepository) ByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return r.first(ctx, "id = ?", id)
}

func (r *gormRepository) first(ctx context.Context, query string, arg any) (*User, error) {
	var user User
	err := r.db.WithContext(ctx).Where(query, arg).First(&user).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("auth: load user: %w", err)
	}
	return &user, nil
}

func (r *gormRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name string) (*User, error) {
	updates := map[string]any{"name": name, "updated_at": time.Now().UTC()}
	return r.update(ctx, id, updates)
}

func (r *gormRepository) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) (*User, error) {
	updates := map[string]any{"password_hash": hash, "updated_at": time.Now().UTC()}
	return r.update(ctx, id, updates)
}

func (r *gormRepository) update(ctx context.Context, id uuid.UUID, updates map[string]any) (*User, error) {
	res := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return nil, fmt.Errorf("auth: update user: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

func (r *gormRepository) TouchLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", id).
		UpdateColumn("last_login_at", at)
	if res.Error != nil {
		return fmt.Errorf("auth: touch last_login_at: %w", res.Error)
	}
	return nil
}

// isUniqueViolation detects a 23505 from the pgx driver. GORM wraps driver errors
// but keeps the chain, so errors.As still finds the PostgreSQL error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == uniqueViolation
	}
	return false
}
