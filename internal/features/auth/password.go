package auth

import (
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the work factor used for every stored password. 12 is the
// documented choice for this project: ~250 ms per hash on the dev machine, which
// is slow enough to hurt offline cracking without hurting login latency.
const BcryptCost = 12

// Hasher wraps bcrypt so the cost can be lowered in tests without touching the
// service.
type Hasher struct {
	cost int

	once  sync.Once
	dummy []byte
}

// NewHasher returns the production hasher.
func NewHasher() *Hasher { return &Hasher{cost: BcryptCost} }

// NewTestHasher returns a deliberately cheap hasher for unit tests only.
func NewTestHasher() *Hasher { return &Hasher{cost: bcrypt.MinCost} }

// Hash derives a bcrypt hash of the password.
func (h *Hasher) Hash(password string) (string, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		// bcrypt rejects passwords longer than 72 bytes; the service validates
		// that first, so this is a programming error rather than user input.
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(digest), nil
}

// Verify reports whether password matches the stored hash. A malformed hash is a
// mismatch, never an error the caller has to handle.
func (h *Hasher) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// CompareDummy spends the same CPU as a real comparison. Login calls it when the
// email is unknown so response time does not reveal which accounts exist.
func (h *Hasher) CompareDummy(password string) {
	h.once.Do(func() {
		h.dummy, _ = bcrypt.GenerateFromPassword([]byte("timing-equalisation-placeholder"), h.cost)
	})
	_ = bcrypt.CompareHashAndPassword(h.dummy, []byte(password))
}
