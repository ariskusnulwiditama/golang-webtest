// Command migrate applies, rolls back and reports the SQL migrations embedded in
// the binary. It is the only writer of the schema; the API never calls AutoMigrate.
//
// Usage:
//
//	go run ./cmd/migrate up          # apply all pending migrations
//	go run ./cmd/migrate down 1      # roll back the last applied migration
//	go run ./cmd/migrate status      # print the recorded version
//
// Logs go to stderr so status output stays script-friendly on stdout.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/database"
	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/logger"
)

const serviceName = "migrate"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.Log, serviceName, os.Stderr)

	switch command := args[0]; command {
	case "up":
		return database.MigrateUp(cfg.DB, log)
	case "down":
		steps := 1
		if len(args) > 1 {
			steps, err = strconv.Atoi(args[1])
			if err != nil || steps < 1 {
				return fmt.Errorf("down expects a positive step count, got %q", args[1])
			}
		}
		return database.MigrateDown(cfg.DB, steps, log)
	case "status":
		return printStatus(cfg)
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("usage: migrate <up|down [n]|status>")
}

func printStatus(cfg config.Config) error {
	version, err := database.MigrationStatus(cfg.DB)
	if err != nil {
		return err
	}
	if !version.Applied {
		fmt.Println("version=none dirty=false applied=0")
		return nil
	}
	fmt.Printf("version=%d dirty=%t applied=1\n", version.Version, version.Dirty)
	return nil
}
