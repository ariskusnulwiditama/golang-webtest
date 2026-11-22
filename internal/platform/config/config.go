// Package config loads and validates process configuration from the environment.
//
// Every process (api, migrate, worker) reads the same variables, so this package
// is the single source of truth for defaults and validation rules. Invalid
// configuration fails fast at startup instead of surfacing later as a confusing
// runtime error (12-factor configuration).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Deployment environments understood by the application.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Config is the complete, validated configuration of one process.
type Config struct {
	Env  string
	HTTP HTTP
	DB   DB
	Log  Log
	Auth Auth
	CORS CORS
}

// HTTP holds the settings of the embedded HTTP server.
type HTTP struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxRequestBody  int64
}

// DB holds PostgreSQL connection settings and pool tuning.
type DB struct {
	Host            string
	Port            int
	Name            string
	User            string
	Password        string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	AutoMigrate     bool
}

// Log holds logging settings.
type Log struct {
	Level  slog.Level
	Format string
	Source bool
}

// Auth holds token signing settings.
type Auth struct {
	JWTSecret      string
	AccessTokenTTL time.Duration
	Issuer         string
}

// CORS holds the browser origins allowed to call the API.
type CORS struct {
	AllowedOrigins []string
}

// IsProduction reports whether production guardrails apply.
func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// Load reads .env when present, then the process environment, and validates the
// result. All validation problems are returned together.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("config: read .env: %w", err)
	}

	l := &loader{}
	cfg := Config{
		Env: l.str("APP_ENV", EnvDevelopment),
		HTTP: HTTP{
			Host:            l.str("HTTP_HOST", "127.0.0.1"),
			Port:            l.integer("HTTP_PORT", 8080),
			ReadTimeout:     l.duration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    l.duration("HTTP_WRITE_TIMEOUT", 60*time.Second),
			IdleTimeout:     l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: l.duration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			MaxRequestBody:  int64(l.integer("HTTP_MAX_REQUEST_BYTES", 1<<20)),
		},
		DB: DB{
			Host:            l.str("DB_HOST", "127.0.0.1"),
			Port:            l.integer("DB_PORT", 5432),
			Name:            l.str("DB_NAME", "webtest"),
			User:            l.str("DB_USER", "webtest"),
			Password:        l.str("DB_PASSWORD", ""),
			SSLMode:         l.str("DB_SSLMODE", "disable"),
			MaxOpenConns:    l.integer("DB_MAX_OPEN_CONNS", 20),
			MaxIdleConns:    l.integer("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: l.duration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			AutoMigrate:     l.boolean("DB_AUTO_MIGRATE", false),
		},
		Log: Log{
			Level:  l.level("LOG_LEVEL", slog.LevelInfo),
			Format: strings.ToLower(l.str("LOG_FORMAT", "json")),
			Source: l.boolean("LOG_SOURCE", false),
		},
		Auth: Auth{
			JWTSecret:      l.str("JWT_SECRET", ""),
			AccessTokenTTL: l.duration("JWT_TTL", 24*time.Hour),
			Issuer:         l.str("JWT_ISSUER", "webtest-studio"),
		},
		CORS: CORS{
			AllowedOrigins: l.list("CORS_ALLOWED_ORIGINS", []string{"http://localhost:5173"}),
		},
	}

	errs := append(append([]error{}, l.errs...), cfg.validate()...)
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loader reads environment variables and records every parse failure so the
// operator sees all problems at once instead of one per restart.
type loader struct {
	errs []error
}

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

func (l *loader) integer(key string, def int) int {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s must be an integer, got %q", key, raw))
		return def
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s must be true or false, got %q", key, raw))
		return def
	}
	return v
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s must be a duration such as 15s or 24h, got %q", key, raw))
		return def
	}
	return v
}

func (l *loader) level(key string, def slog.Level) slog.Level {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	l.errs = append(l.errs, fmt.Errorf("config: %s must be one of debug, info, warn, error; got %q", key, raw))
	return def
}

func (l *loader) list(key string, def []string) []string {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
