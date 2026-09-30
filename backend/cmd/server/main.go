// Command server runs the TripVault API.
//
// With no flags it serves HTTP. With -migrate-only it applies, rolls back or
// reports migrations and exits, so a deployment can migrate before starting
// traffic without needing a separate migration binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/emailserver"
	"github.com/tripvault/backend/internal/server"
	"github.com/tripvault/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet, so fall back to stderr directly.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// run contains the real work so main stays a single exit point.
func run() error {
	migrateOnly := flag.String("migrate-only", "",
		"run migrations and exit: up, down, status or reset")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	if *migrateOnly != "" {
		return runMigrations(cfg, *migrateOnly, log)
	}
	return runServer(cfg, log)
}

// newLogger builds the structured logger.
//
// JSON in production so log aggregators can parse it, human readable
// everywhere else so a developer can read a terminal.
func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if cfg.App.IsProduction() {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	logger := slog.New(handler).With("service", cfg.App.Name)
	if cfg.App.IsProduction() {
		// Never log credentials. In other environments the value aids debugging.
		logger = logger.With("env", cfg.App.Env)
	} else {
		logger = logger.With("env", cfg.App.Env, "color_mode", "auto")
	}
	return logger
}

// runMigrations handles -migrate-only.
func runMigrations(cfg *config.Config, mode string, log *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := database.NewMigrator(pool, log)
	if err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "up":
		return migrator.MigrateUp(ctx)
	case "down":
		return migrator.MigrateDown(ctx)
	case "status":
		return printMigrationStatus(ctx, migrator)
	case "reset":
		log.Warn("resetting database schema")
		return migrator.MigrateReset(ctx)
	default:
		return fmt.Errorf("unknown -migrate-only value %q: want up, down, status or reset", mode)
	}
}

// printMigrationStatus writes the applied state to stdout for humans.
func printMigrationStatus(ctx context.Context, migrator *database.Migrator) error {
	statuses, err := migrator.Status(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("%-8s %-28s %-10s %s\n", "VERSION", "NAME", "STATE", "APPLIED AT")
	for _, status := range statuses {
		state := "pending"
		appliedAt := "-"
		if status.Applied {
			state = "applied"
			appliedAt = status.AppliedAt
		}
		fmt.Printf("%-8d %-28s %-10s %s\n", status.Version, status.Name, state, appliedAt)
	}
	return nil
}

// runServer wires every dependency and serves until interrupted.
func runServer(cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Redis backs rate limiting and caching. It is not a system of record, so a
	// failure to reach it degrades the service rather than preventing startup.
	redisStore, err := storage.NewRedis(ctx, cfg.Redis, log)
	if err != nil {
		log.Warn("redis unavailable, running without rate limiting", "error", err)
		redisStore = nil
	} else {
		defer func() { _ = redisStore.Close() }()
	}

	mailer := emailserver.New(cfg.SMTP, log)
	defer mailer.Close()
	if !mailer.Enabled() {
		log.Warn("smtp disabled; verification and reset emails will not be delivered")
	}

	srv, err := server.New(server.Deps{
		Config: cfg,
		DB:     pool,
		Redis:  redisStore,
		Logger: log,
		Mailer: mailer,
	})
	if err != nil {
		return err
	}

	// Fail fast on an unmigrated database. Starting up against an old schema
	// produces confusing errors on the first request instead of here.
	if err := ensureSchema(ctx, pool, log); err != nil {
		return err
	}

	// Expired tokens are kept so that reuse detection still works, but only until
	// they can no longer authenticate anything. Without a sweeper both tables
	// grow without bound.
	go pruneExpiredTokens(ctx, srv, log, cfg.JWT.RefreshExpiration+time.Hour)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// srv.Run drains the listener before returning; waiting on it here
		// keeps in-flight requests from being cut off mid-response.
		return <-errCh
	}
}

// pruneExpiredTokens deletes tokens that have outlived any possible use.
//
// Refresh tokens are swept after a full refresh lifetime has passed since they
// were issued, so a token is only deleted once no client could still present it.
func pruneExpiredTokens(ctx context.Context, srv *server.Server, log *slog.Logger, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			auth, refresh, err := srv.DeleteExpiredTokens(ctx)
			if err != nil {
				// A failed sweep is not worth stopping the service over; the next
				// tick tries again.
				log.Warn("token sweep failed", "error", err)
				continue
			}
			if auth > 0 || refresh > 0 {
				log.Info("pruned expired tokens", "auth_tokens", auth, "refresh_tokens", refresh)
			}
		}
	}
}

// ensureSchema verifies the database is migrated far enough to serve requests.
func ensureSchema(ctx context.Context, pool *database.Pool, log *slog.Logger) error {
	var applied bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = 'users')`).Scan(&applied)
	if err != nil {
		// The check is advisory: if it cannot run, say so rather than refusing
		// to start, because the server may still serve /ready correctly.
		log.Warn("could not verify schema state", "error", err)
		return nil
	}
	if !applied {
		return errors.New("database is not migrated: run `make migrate-up` before starting the server")
	}
	log.Info("schema verified")
	return nil
}
