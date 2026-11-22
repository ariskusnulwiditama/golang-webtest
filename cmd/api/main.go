// Command api is the WebTest Studio HTTP server.
//
// Dependencies are wired by hand in this file (no DI framework): the chain is
// config → logger → database → router → http.Server, with a graceful shutdown on
// SIGINT/SIGTERM so in-flight requests finish and the pool closes cleanly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/apperr"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/database"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/health"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/httpx"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/logger"
)

// version is injected at build time:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/api
var version = "dev"

const (
	serviceName       = "api"
	readinessTimeout  = 2 * time.Second
	readHeaderTimeout = 10 * time.Second
	maxHeaderBytes    = 1 << 20 // 1 MiB
)

func main() {
	if err := run(); err != nil {
		// Configuration may have failed before a logger existed.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log, serviceName, os.Stdout)
	slog.SetDefault(log)

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	log.Info("starting",
		slog.String("env", cfg.Env),
		slog.String("version", version),
		slog.String("addr", cfg.HTTP.Addr()),
	)

	// Cancelled on Ctrl+C or SIGTERM (SIGTERM is what containers send).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.DB.AutoMigrate {
		// Development/compose convenience only (config validation rejects it in
		// production); the real deployments run cmd/migrate as their own step.
		if err := database.MigrateUp(cfg.DB, log); err != nil {
			return err
		}
	}

	db, err := database.Open(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("closing database", slog.Any("error", err))
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr(),
		Handler:           buildRouter(cfg, log, db),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutdown requested", slog.Duration("timeout", cfg.HTTP.ShutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

// buildRouter assembles the middleware chain and the routes that exist today.
// Feature modules register their routes here as they land.
func buildRouter(cfg config.Config, log *slog.Logger, db *database.DB) *gin.Engine {
	router := gin.New()

	// Order matters: request id first so every later log line can reference it,
	// then observability, then safety nets.
	router.Use(
		httpx.RequestID(),
		httpx.AccessLog(log),
		httpx.Recovery(log),
		httpx.CORS(cfg.CORS.AllowedOrigins),
		httpx.BodyLimit(cfg.HTTP.MaxRequestBody),
	)

	router.HandleMethodNotAllowed = true

	health.New(log, version, readinessTimeout, map[string]health.Checker{
		"database": db,
	}).Register(router)

	router.NoRoute(func(c *gin.Context) {
		httpx.Fail(c, log, apperr.NotFound("route"))
	})
	router.NoMethod(func(c *gin.Context) {
		httpx.Fail(c, log, apperr.MethodNotAllowed(c.Request.Method, c.Request.URL.Path))
	})

	return router
}
