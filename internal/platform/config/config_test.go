package config_test

import (
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
)

// baseEnv provides the minimum every environment must define and blanks the
// optional variables so that machine-level settings cannot leak into a test.
func baseEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"APP_ENV", "HTTP_PORT", "HTTP_HOST", "DB_PASSWORD", "DB_PORT", "DB_HOST",
		"DB_NAME", "DB_USER", "DB_SSLMODE", "DB_AUTO_MIGRATE", "JWT_SECRET", "JWT_TTL",
		"LOG_LEVEL", "LOG_FORMAT", "CORS_ALLOWED_ORIGINS",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DB_PASSWORD", "local-secret")
	t.Setenv("JWT_SECRET", strings.Repeat("k", 40))
}

func TestLoadAppliesDefaults(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an unexpected error: %v", err)
	}

	if cfg.Env != config.EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.Env, config.EnvDevelopment)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("HTTP.Port = %d, want 8080", cfg.HTTP.Port)
	}
	if got, want := cfg.HTTP.Addr(), "127.0.0.1:8080"; got != want {
		t.Errorf("HTTP.Addr() = %q, want %q", got, want)
	}
	if cfg.DB.Port != 5432 {
		t.Errorf("DB.Port = %d, want 5432", cfg.DB.Port)
	}
	if cfg.DB.SSLMode != "disable" {
		t.Errorf("DB.SSLMode = %q, want disable", cfg.DB.SSLMode)
	}
	if cfg.Auth.AccessTokenTTL != 24*time.Hour {
		t.Errorf("Auth.AccessTokenTTL = %s, want 24h", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Log.Level != slog.LevelInfo {
		t.Errorf("Log.Level = %s, want INFO", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("Log.Format = %q, want json", cfg.Log.Format)
	}
	if len(cfg.CORS.AllowedOrigins) != 1 || cfg.CORS.AllowedOrigins[0] != "http://localhost:5173" {
		t.Errorf("CORS.AllowedOrigins = %v, want [http://localhost:5173]", cfg.CORS.AllowedOrigins)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing jwt secret", map[string]string{"JWT_SECRET": " "}, "JWT_SECRET is required"},
		{"short jwt secret", map[string]string{"JWT_SECRET": "too-short"}, "must be at least 32 characters"},
		{"non numeric port", map[string]string{"HTTP_PORT": "eighty"}, "HTTP_PORT must be an integer"},
		{"unknown environment", map[string]string{"APP_ENV": "staging"}, "APP_ENV must be one of"},
		{"bad duration", map[string]string{"JWT_TTL": "one day"}, "JWT_TTL must be a duration"},
		{"unknown log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL must be one of"},
		{"bad sslmode", map[string]string{"DB_SSLMODE": "on"}, "DB_SSLMODE must be one of"},
		{"missing db password", map[string]string{"DB_PASSWORD": " "}, "DB_PASSWORD must not be empty"},
		{"idle exceeds open", map[string]string{"DB_MAX_OPEN_CONNS": "2", "DB_MAX_IDLE_CONNS": "5"}, "DB_MAX_IDLE_CONNS must be between"},
		{
			"production automigrate",
			map[string]string{"APP_ENV": config.EnvProduction, "DB_AUTO_MIGRATE": "true", "DB_SSLMODE": "require"},
			"DB_AUTO_MIGRATE must be false in production",
		},
		{
			"production wildcard cors",
			map[string]string{"APP_ENV": config.EnvProduction, "DB_SSLMODE": "require", "CORS_ALLOWED_ORIGINS": "*"},
			"must not contain * in production",
		},
		{
			"production without tls",
			map[string]string{"APP_ENV": config.EnvProduction, "DB_SSLMODE": "disable"},
			"DB_SSLMODE must not be disable in production",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	baseEnv(t)
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("HTTP_PORT", "0")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() succeeded, want an error")
	}
	for _, want := range []string{"JWT_SECRET", "HTTP_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error %q does not mention %s", err, want)
		}
	}
}

func TestDSNRoundTripsSpecialCharactersInPassword(t *testing.T) {
	db := config.DB{
		Host: "127.0.0.1", Port: 5432, Name: "webtest",
		User: "webtest", Password: `p@ss:w/rd?x#1`, SSLMode: "disable",
	}

	parsed, err := url.Parse(db.DSN())
	if err != nil {
		t.Fatalf("DSN() produced an unparsable URL: %v", err)
	}
	if got, want := parsed.Scheme, "postgres"; got != want {
		t.Errorf("scheme = %q, want %q", got, want)
	}
	if got, want := parsed.User.Username(), "webtest"; got != want {
		t.Errorf("user = %q, want %q", got, want)
	}
	password, _ := parsed.User.Password()
	if password != db.Password {
		t.Errorf("password = %q, want %q", password, db.Password)
	}
	if got, want := parsed.Host, "127.0.0.1:5432"; got != want {
		t.Errorf("host = %q, want %q", got, want)
	}
	if got, want := parsed.Path, "/webtest"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Errorf("sslmode = %q, want disable", got)
	}

	redacted := db.RedactedDSN()
	if strings.Contains(redacted, "p%40ss") || strings.Contains(redacted, db.Password) {
		t.Errorf("RedactedDSN() leaks the password: %q", redacted)
	}
}
