package auth

import (
	"strings"
	"testing"
)

func TestHasherRoundTrip(t *testing.T) {
	h := NewTestHasher()
	const password = "correct horse battery"

	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == password {
		t.Fatal("hash equals the plaintext")
	}
	if !h.Verify(hash, password) {
		t.Error("Verify rejected the original password")
	}
	if h.Verify(hash, password+"!") {
		t.Error("Verify accepted a different password")
	}
	if h.Verify(hash, strings.ToUpper(password)) {
		t.Error("Verify must be case sensitive")
	}
}

func TestProductionCostIsTwelve(t *testing.T) {
	h := NewHasher()
	if h.cost != BcryptCost {
		t.Fatalf("cost = %d, want %d", h.cost, BcryptCost)
	}

	hash, err := h.Hash("a-long-enough-password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	// bcrypt encodes the cost in the hash prefix: $2a$12$...
	if !strings.HasPrefix(hash, "$2a$12$") {
		t.Errorf("hash %q does not carry cost 12", hash)
	}
}

func TestVerifyMalformedHashIsAMismatch(t *testing.T) {
	h := NewTestHasher()
	if h.Verify("not-a-bcrypt-hash", "whatever") {
		t.Fatal("malformed hash must never verify")
	}
}

func TestHashRejectsOverlongPassword(t *testing.T) {
	h := NewTestHasher()
	if _, err := h.Hash(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
		t.Fatal("expected bcrypt to reject a password longer than 72 bytes")
	}
}

func TestCompareDummySpendsWorkWithoutPanicking(t *testing.T) {
	h := NewTestHasher()
	h.CompareDummy("anything")
	h.CompareDummy("anything")
	if h.dummy == nil {
		t.Fatal("dummy hash was not generated")
	}
}

func TestNewTestHasherIsCheap(t *testing.T) {
	if got := NewTestHasher().cost; got >= BcryptCost {
		t.Fatalf("test cost = %d; tests must not pay production cost", got)
	}
}
