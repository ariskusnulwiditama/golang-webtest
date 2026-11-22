package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
	"github.com/ariskusnulwiditama/golang-webtest/migrations"
)

// MigrationVersion is the schema version recorded in schema_migrations.
type MigrationVersion struct {
	Version uint
	Dirty   bool
	// Applied is false when the database has never run a migration.
	Applied bool
}

// newMigrator wires the embedded SQL files to a dedicated single-connection
// pool, so a migration can never deadlock against serving traffic.
func newMigrator(cfg config.DB) (*migrate.Migrate, func(), error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("migrations: read embedded files: %w", err)
	}

	pool, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, nil, fmt.Errorf("migrations: open connection: %w", err)
	}
	pool.SetMaxOpenConns(1)

	driver, err := pgxmigrate.WithInstance(pool, &pgxmigrate.Config{})
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrations: build driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrations: initialise: %w", err)
	}

	// Close releases both the source and the pool it owns.
	cleanup := func() { _, _ = migrator.Close() }
	return migrator, cleanup, nil
}

func recordedVersion(m *migrate.Migrate) (MigrationVersion, error) {
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return MigrationVersion{}, nil
	}
	if err != nil {
		return MigrationVersion{}, fmt.Errorf("migrations: read version: %w", err)
	}
	return MigrationVersion{Version: version, Dirty: dirty, Applied: true}, nil
}

// MigrateUp applies every pending migration and is a no-op when up to date.
func MigrateUp(cfg config.DB, log *slog.Logger) error {
	migrator, cleanup, err := newMigrator(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	before, err := recordedVersion(migrator)
	if err != nil {
		return err
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrations: apply: %w", err)
	}

	after, err := recordedVersion(migrator)
	if err != nil {
		return err
	}
	if after.Version == before.Version && after.Applied == before.Applied {
		log.Info("migrations already up to date", slog.Uint64("version", uint64(after.Version)))
		return nil
	}
	log.Info("migrations applied",
		slog.Uint64("from", uint64(before.Version)),
		slog.Uint64("to", uint64(after.Version)),
	)
	return nil
}

// MigrateDown rolls back the last `steps` applied migrations.
func MigrateDown(cfg config.DB, steps int, log *slog.Logger) error {
	if steps < 1 {
		return fmt.Errorf("migrations: rollback step count must be >= 1, got %d", steps)
	}

	migrator, cleanup, err := newMigrator(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	before, err := recordedVersion(migrator)
	if err != nil {
		return err
	}
	if !before.Applied {
		log.Info("nothing to roll back")
		return nil
	}

	if err := migrator.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrations: rollback: %w", err)
	}

	after, err := recordedVersion(migrator)
	if err != nil {
		return err
	}
	log.Info("migrations rolled back",
		slog.Uint64("from", uint64(before.Version)),
		slog.Uint64("to", uint64(after.Version)),
		slog.Int("steps", steps),
	)
	return nil
}

// MigrationStatus reports the recorded schema version without changing anything.
func MigrationStatus(cfg config.DB) (MigrationVersion, error) {
	migrator, cleanup, err := newMigrator(cfg)
	if err != nil {
		return MigrationVersion{}, err
	}
	defer cleanup()

	return recordedVersion(migrator)
}
