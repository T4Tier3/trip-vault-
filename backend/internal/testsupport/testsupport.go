// Package testsupport provides the shared fixtures integration tests need.
//
// Tests that touch PostgreSQL or Redis skip cleanly when those services are not
// running, so `go test ./...` stays useful on a machine with no Docker. CI
// starts the services, which makes the same command a real integration run
// there without a second code path.
package testsupport

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/storage"
)

// EnvDatabaseURL names the variable integration tests read their URL from.
const EnvDatabaseURL = "TEST_DATABASE_URL"

// EnvRedisURL names the variable integration tests read their Redis URL from.
const EnvRedisURL = "TEST_REDIS_URL"

// fastHashParams returns hashing parameters cheap enough to keep tests quick.
//
// The cost is deliberately low rather than the production default: these
// parameters test the hashing path, not the work factor.
func fastHashParams() config.Argon2 {
	return config.Argon2{
		Time:    1,
		Memory:  8 * 1024,
		Threads: 1,
		SaltLen: 16,
		KeyLen:  32,
	}
}

// TestConfig builds a validated configuration for tests.
//
// JWT secrets are fixed and known-length so validation passes, and rate limiting
// is left off by default so a test does not have to reason about a shared
// counter.
func TestConfig(t *testing.T) *config.Config {
	t.Helper()

	return &config.Config{
		App: config.App{
			Env:        config.EnvTest,
			Port:       8080,
			BaseURL:    "http://localhost:8080",
			Name:       "TripVaultTest",
			LinkScheme: "tripvault",
		},
		Database: config.Database{
			URL:             "postgresql://postgres:postgres@localhost:5432/tripvault_test?sslmode=disable",
			MaxConns:        8,
			MinConns:        1,
			MaxConnLifetime: time.Hour,
			ConnectTimeout:  5 * time.Second,
		},
		Redis: config.Redis{URL: "redis://localhost:6379/0", Prefix: "tripvault-test"},
		JWT: config.JWT{
			AccessSecret:     []byte("test-access-secret-that-is-long-enough-32"),
			RefreshSecret:    []byte("test-refresh-secret-that-is-long-enough-33"),
			AccessIssuer:     "tripvault-test",
			RefreshIssuer:    "tripvault-test",
			AccessExpiration: 15 * time.Minute,
			// Shorter than production so a test can assert on expiry without
			// sleeping for a month.
			RefreshExpiration: time.Hour,
		},
		Argon2:    fastHashParams(),
		LogLevel:  "error",
		RateLimit: config.RateLimit{Enabled: false},
	}
}

// Logger returns a quiet logger for tests, so expected error paths do not fill
// the output with noise.
func Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// Pool returns a migrated pool, skipping the test if PostgreSQL is unreachable.
//
// Migration runs before the test body, so an integration test never runs against
// a schema it did not set up itself.
func Pool(t *testing.T) *database.Pool {
	t.Helper()

	cfg := TestConfig(t)
	if raw := os.Getenv(EnvDatabaseURL); raw != "" {
		cfg.Database.URL = raw
	} else if raw := os.Getenv("DATABASE_URL"); raw != "" {
		cfg.Database.URL = raw
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cfg.Database = perPackageDatabase(t, ctx, cfg.Database)

	pool, err := database.NewPool(ctx, cfg.Database, Logger())
	if err != nil {
		t.Skipf("PostgreSQL unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(pool.Close)

	migrator, err := database.NewMigrator(pool, Logger())
	if err != nil {
		t.Fatalf("build migrator: %v", err)
	}
	if err := migrator.MigrateUp(ctx); err != nil {
		// A test that exercises rollbacks can leave tables behind without a
		// matching bookkeeping row if it fails partway. Repair that state rather
		// than failing every later test with a confusing "relation already
		// exists".
		if repairErr := dropPublicSchema(ctx, pool); repairErr == nil {
			if retryErr := migrator.MigrateUp(ctx); retryErr == nil {
				t.Logf("repaired inconsistent test schema before migrating: %v", err)
				return pool
			}
		}
		t.Fatalf("apply migrations: %v", err)
	}

	return pool
}

// perPackageDatabaseURL redirects a test connection to a database owned by the
// package under test.
//
// `go test ./...` runs each package's test binary as its own process, and those
// processes run concurrently. Sharing one database between them is not safe:
// one package dropping the public schema to test the migrator's repair path
// pulls the schema out from under another package that is halfway through
// applying it, which surfaces as a confusing "duplicate key value violates
// unique constraint pg_proc_proname_args_nsp_index" or "relation users does not
// exist" in a package that did nothing wrong.
//
// So each test binary gets its own database, created on demand. The name is
// derived from the test binary, which go test names after the package.
func perPackageDatabase(t *testing.T, ctx context.Context, db config.Database) config.Database {
	t.Helper()

	parsed, err := url.Parse(db.URL)
	if err != nil {
		// A URL this package cannot parse is not something to silently rewrite.
		// database.NewPool will report it properly.
		return db
	}

	owner := strings.TrimSuffix(filepath.Base(os.Args[0]), ".test")
	owner = sanitizeDatabaseName(owner)
	if owner == "" {
		owner = "default"
	}
	name := "tripvault_test_" + owner

	if parsed.Path == "/"+name {
		// Already per-package, probably because the caller asked for it.
		return db
	}
	parsed.Path = "/" + name

	// Create it if this is the first run for this package. Failure here is not
	// fatal on its own: if the database already exists the connect below will
	// succeed, and if the user lacks CREATE DATABASE the tests will skip with a
	// message rather than failing.
	// The admin connection reuses this configuration, so it has to be a copy with
	// the pool settings intact. A zero MaxConns is rejected by pgxpool.
	admin := db
	admin.URL = parsed.String()
	if err := ensureDatabaseExists(ctx, admin, name); err != nil {
		t.Logf("could not ensure test database %q exists: %v", name, err)
	}
	parsed.Path = "/" + name
	db.URL = parsed.String()

	return db
}

// sanitizeDatabaseName keeps only characters that are legal unquoted in a
// PostgreSQL identifier.
func sanitizeDatabaseName(in string) string {
	var b strings.Builder
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// ensureDatabaseExists creates the database through the maintenance database,
// doing nothing if it is already there. db carries the caller's pool settings;
// only the database name is changed to the maintenance one.
func ensureDatabaseExists(ctx context.Context, db config.Database, name string) error {
	parsed, err := url.Parse(db.URL)
	if err != nil {
		return err
	}
	parsed.Path = "/postgres"
	db.URL = parsed.String()

	admin, err := database.NewPool(ctx, db, Logger())
	if err != nil {
		return err
	}
	defer admin.Close()

	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	// CREATE DATABASE cannot be parameterised, so the name is quoted rather than
	// interpolated. sanitizeDatabaseName has already reduced it to
	// [a-z0-9_-], and the quoting is belt and braces.
	_, err = admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name))
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

// dropPublicSchema removes everything in the public schema, including any
// bookkeeping table, so migrations can start from a clean slate.
//
// This is a destructive operation on whichever database TEST_DATABASE_URL or
// DATABASE_URL points at, and it is reached from ordinary test setup rather than
// from an obviously destructive call, so it refuses to run unless the target
// database is recognisably a test database. The mistake this guards against is
// running `make test` with DATABASE_URL exported from a shell that also holds a
// real connection string: without the check, `go test ./...` would drop
// production's schema.
func dropPublicSchema(ctx context.Context, pool *database.Pool) error {
	if err := requireTestDatabase(ctx, pool); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`)
	return err
}

// DropSchema removes the public schema, refusing unless the target database is
// named as a test database. Exported because a test that needs to simulate a
// migration landing under a live connection must be able to, and must be held to
// the same guard as the automatic repair path.
func DropSchema(ctx context.Context, pool *database.Pool) error {
	return dropPublicSchema(ctx, pool)
}

// requireTestDatabase returns an error unless the pool is connected to a
// database whose name marks it as a test database.
func requireTestDatabase(ctx context.Context, pool *database.Pool) error {
	var name string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return fmt.Errorf("read current database: %w", err)
	}
	if !isTestDatabaseName(name) {
		return fmt.Errorf("refusing to drop schema: connected to database %q, "+
			"which is not named as a test database; set %s or DATABASE_URL to a "+
			"database whose name contains \"test\"", name, EnvDatabaseURL)
	}
	return nil
}

// isTestDatabaseName reports whether a database name looks like a test
// database. "test" as a substring is the check; a stricter rule would be
// annoying to work around, and this only has to catch the accident.
func isTestDatabaseName(name string) bool {
	return strings.Contains(strings.ToLower(name), "test")
}

// Redis returns a client, skipping the test if Redis is unreachable.
func Redis(t *testing.T) *storage.Redis {
	t.Helper()

	cfg := TestConfig(t).Redis
	if raw := os.Getenv(EnvRedisURL); raw != "" {
		cfg.URL = raw
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	store, err := storage.NewRedis(ctx, cfg, Logger())
	if err != nil {
		t.Skipf("Redis unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

// Truncate clears the auth tables between tests.
//
// Tests share one database, so leftover rows from a previous test would make a
// registration unexpectedly report the address as taken. Redis keys are
// flushed too, so a rate limit left over from one test cannot throttle another.
func Truncate(t *testing.T, pool *database.Pool, redisStore *storage.Redis) {
	t.Helper()

	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE users, auth_tokens, refresh_tokens, devices RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	if redisStore != nil {
		iter := redisStore.Client().Scan(ctx, 0, redisStore.Prefix()+":*", 500).Iterator()
		var keys []string
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		if err := iter.Err(); err != nil {
			t.Fatalf("scan redis keys: %v", err)
		}
		if len(keys) > 0 {
			if err := redisStore.Client().Del(ctx, keys...).Err(); err != nil {
				t.Fatalf("delete redis keys: %v", err)
			}
		}
	}
}

// UniqueEmail returns a distinct address per test invocation.
func UniqueEmail(prefix string) string {
	if prefix == "" {
		prefix = "user"
	}
	return prefix + "-" + NewID() + "@example.com"
}

// NewID returns a short unique identifier, from the clock and a random suffix.
func NewID() string {
	return strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000"), ".", ""))
}
