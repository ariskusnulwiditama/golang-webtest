// Package auth implements account registration, login, JWT access tokens and the
// middleware that authenticates requests (Phase 2).
//
// Token model: HS256 access tokens only, no refresh tokens, logout is
// client-side (AD-14). The Tokenizer is the only place that knows how a token is
// signed, so adding refresh tokens later does not touch the feature code.
package auth

import (
	"time"

	"github.com/google/uuid"
)

// User is the GORM mapping of the users table. The schema is owned by the
// migrations in migrations/000001_init.up.sql; this struct never changes it.
type User struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Email        string     `gorm:"column:email;not null"`
	PasswordHash string     `gorm:"column:password_hash;not null"`
	Name         string     `gorm:"column:name;not null"`
	Role         string     `gorm:"column:role;not null"`
	IsActive     bool       `gorm:"column:is_active;not null"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the mapping to the real table name.
func (User) TableName() string { return "users" }

// PublicUser is the API representation of an account. The password hash is
// structurally impossible to leak because it is not part of this type.
type PublicUser struct {
	ID          uuid.UUID  `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	IsActive    bool       `json:"is_active"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Public converts the stored user into its safe representation.
func (u *User) Public() PublicUser {
	return PublicUser{
		ID:          u.ID,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		IsActive:    u.IsActive,
		LastLoginAt: u.LastLoginAt,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}
