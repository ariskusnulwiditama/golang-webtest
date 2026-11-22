package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

const (
	// MinJWTSecretLength is the shortest signing key accepted; HS256 keys shorter
	// than the 256-bit hash output weaken the signature.
	MinJWTSecretLength = 32
)

var validSSLModes = map[string]struct{}{
	"disable":     {},
	"allow":       {},
	"prefer":      {},
	"require":     {},
	"verify-ca":   {},
	"verify-full": {},
}

// validate collects every problem instead of stopping at the first one.
func (c Config) validate() []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	switch c.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		add("config: APP_ENV must be one of %s, %s, %s; got %q",
			EnvDevelopment, EnvTest, EnvProduction, c.Env)
	}

	if c.HTTP.Host == "" {
		add("config: HTTP_HOST must not be empty")
	}
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		add("config: HTTP_PORT must be between 1 and 65535, got %d", c.HTTP.Port)
	}
	if c.HTTP.ShutdownTimeout <= 0 {
		add("config: HTTP_SHUTDOWN_TIMEOUT must be greater than zero, got %s", c.HTTP.ShutdownTimeout)
	}
	if c.HTTP.MaxRequestBody < 1 {
		add("config: HTTP_MAX_REQUEST_BYTES must be greater than zero, got %d", c.HTTP.MaxRequestBody)
	}

	if c.DB.Host == "" {
		add("config: DB_HOST must not be empty")
	}
	if c.DB.Port < 1 || c.DB.Port > 65535 {
		add("config: DB_PORT must be between 1 and 65535, got %d", c.DB.Port)
	}
	if c.DB.Name == "" {
		add("config: DB_NAME must not be empty")
	}
	if c.DB.User == "" {
		add("config: DB_USER must not be empty")
	}
	if c.DB.Password == "" {
		add("config: DB_PASSWORD must not be empty")
	}
	if _, ok := validSSLModes[c.DB.SSLMode]; !ok {
		add("config: DB_SSLMODE must be one of disable, allow, prefer, require, verify-ca, verify-full; got %q", c.DB.SSLMode)
	}
	if c.DB.MaxOpenConns < 1 {
		add("config: DB_MAX_OPEN_CONNS must be at least 1, got %d", c.DB.MaxOpenConns)
	}
	if c.DB.MaxIdleConns < 0 || c.DB.MaxIdleConns > c.DB.MaxOpenConns {
		add("config: DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS (%d), got %d",
			c.DB.MaxOpenConns, c.DB.MaxIdleConns)
	}
	if c.DB.ConnMaxLifetime <= 0 {
		add("config: DB_CONN_MAX_LIFETIME must be greater than zero, got %s", c.DB.ConnMaxLifetime)
	}

	if c.Log.Format != "json" && c.Log.Format != "text" {
		add("config: LOG_FORMAT must be json or text, got %q", c.Log.Format)
	}

	switch {
	case c.Auth.JWTSecret == "":
		add("config: JWT_SECRET is required (generate one with at least %d random characters)", MinJWTSecretLength)
	case len(c.Auth.JWTSecret) < MinJWTSecretLength:
		add("config: JWT_SECRET must be at least %d characters, got %d", MinJWTSecretLength, len(c.Auth.JWTSecret))
	}
	if c.Auth.AccessTokenTTL <= 0 {
		add("config: JWT_TTL must be greater than zero, got %s", c.Auth.AccessTokenTTL)
	}
	if c.Auth.Issuer == "" {
		add("config: JWT_ISSUER must not be empty")
	}

	if len(c.CORS.AllowedOrigins) == 0 && c.Env != EnvTest {
		add("config: CORS_ALLOWED_ORIGINS must list at least one origin (use * only in development)")
	}
	if c.IsProduction() {
		if c.DB.AutoMigrate {
			add("config: DB_AUTO_MIGRATE must be false in production; run cmd/migrate instead")
		}
		for _, origin := range c.CORS.AllowedOrigins {
			if origin == "*" {
				add("config: CORS_ALLOWED_ORIGINS must not contain * in production")
			}
		}
		if c.DB.SSLMode == "disable" {
			add("config: DB_SSLMODE must not be disable in production")
		}
	}

	return errs
}

// Addr is the host:port the HTTP server listens on.
func (c HTTP) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// DSN builds the libpq/pgx connection URL.
func (c DB) DSN() string {
	return c.dsn(c.Password)
}

// RedactedDSN is safe to log: the password is replaced.
func (c DB) RedactedDSN() string {
	return c.dsn("xxxxx")
}

func (c DB) dsn(password string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Name,
	}
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	q.Set("application_name", "webtest-studio")
	u.RawQuery = q.Encode()
	return u.String()
}
