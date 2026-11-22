// Package database owns the PostgreSQL connection of the API process.
//
// Repositories receive the GORM handle; the raw pool stays private so only this
// package decides about pooling, timeouts and liveness checks.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
)

const (
	// slowQueryThreshold is when GORM starts warning about a statement.
	slowQueryThreshold = 500 * time.Millisecond
	// connMaxIdleTime recycles idle connections before the server closes them.
	connMaxIdleTime = 5 * time.Minute
)

// DB bundles the repository handle and the underlying pool.
type DB struct {
	// Gorm is used by repositories.
	Gorm *gorm.DB

	pool *sql.DB
}

// Open connects to PostgreSQL, tunes the pool and verifies the connection so a
// misconfigured database fails at startup rather than on the first request.
func Open(ctx context.Context, cfg config.DB, log *slog.Logger) (*DB, error) {
	gormDB, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		// Migrations are the only way to change the schema; GORM must never do it.
		DisableForeignKeyConstraintWhenMigrating: true,
		PrepareStmt:                              true,
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
		Logger: gormlogger.New(gormWriter{log: log}, gormlogger.Config{
			SlowThreshold:             slowQueryThreshold,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("database: open %s: %w", cfg.RedactedDSN(), err)
	}

	pool, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("database: obtain pool: %w", err)
	}
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	pool.SetMaxIdleConns(cfg.MaxIdleConns)
	pool.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	pool.SetConnMaxIdleTime(connMaxIdleTime)

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping %s: %w", cfg.RedactedDSN(), err)
	}

	log.Info("database connected",
		slog.String("dsn", cfg.RedactedDSN()),
		slog.Int("max_open_conns", cfg.MaxOpenConns),
		slog.Int("max_idle_conns", cfg.MaxIdleConns),
	)

	return &DB{Gorm: gormDB, pool: pool}, nil
}

// PingContext reports whether the database is reachable; used by /readyz.
func (db *DB) PingContext(ctx context.Context) error {
	return db.pool.PingContext(ctx)
}

// Stats exposes pool counters for diagnostics.
func (db *DB) Stats() sql.DBStats { return db.pool.Stats() }

// Close releases every pooled connection.
func (db *DB) Close() error { return db.pool.Close() }

// gormWriter adapts GORM's logger to slog so queries appear in the same stream
// as everything else, including the request-independent service name.
type gormWriter struct{ log *slog.Logger }

func (w gormWriter) Printf(format string, args ...any) {
	w.log.Warn(strings.TrimSpace(fmt.Sprintf(format, args...)), slog.String("component", "gorm"))
}
