package migrations_test

import (
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/ariskusnulwiditama/golang-webtest/migrations"
)

// TestEmbeddedMigrationsAreCompletePairs guards the packaging contract: every
// version must ship both an up and a down file, otherwise `migrate down` breaks
// in environments that only have the compiled binary.
func TestEmbeddedMigrationsAreCompletePairs(t *testing.T) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("embedded migrations are unreadable: %v", err)
	}

	version, err := source.First()
	if err != nil {
		t.Fatalf("no migration files embedded: %v", err)
	}
	if version != 1 {
		t.Errorf("first migration version = %d, want 1", version)
	}

	seen := make(map[uint]bool)
	for {
		if seen[version] {
			t.Fatalf("migration version %d appears twice", version)
		}
		seen[version] = true

		up, _, err := source.ReadUp(version)
		if err != nil {
			t.Fatalf("version %d has no up file: %v", version, err)
		}
		_ = up.Close()

		down, _, err := source.ReadDown(version)
		if err != nil {
			t.Fatalf("version %d has no down file: %v", version, err)
		}
		_ = down.Close()

		next, err := source.Next(version)
		if err != nil {
			if !os.IsNotExist(err) {
				t.Fatalf("walking migrations after version %d: %v", version, err)
			}
			break
		}
		version = next
	}

	if len(seen) != 1 {
		t.Errorf("embedded migration count = %d, want 1", len(seen))
	}
}
