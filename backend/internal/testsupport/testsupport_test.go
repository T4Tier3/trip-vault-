package testsupport_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/testsupport"
)

// EnvDevDatabaseURL points at a database that is deliberately NOT named as a
// test database, so the destructive-path guard can be exercised against it.
const EnvDevDatabaseURL = "TESTDEV_DATABASE_URL"

// TestDropSchema_RefusesNonTestDatabase guards the destructive path.
//
// The repair path that runs on every test setup drops the public schema, so it
// needs a guard: without one, `go test ./...` in a shell that happens to export
// DATABASE_URL would wipe whatever that URL points at. This is a small guard
// against a very large mistake, and it is worth a test because the failure mode
// is someone else's data.
//
// The pool here is built directly rather than through Pool, because Pool
// deliberately rewrites the URL to a per-package database whose name contains
// "test", which is exactly the case the guard permits.
func TestDropSchema_RefusesNonTestDatabase(t *testing.T) {
	raw := os.Getenv(EnvDevDatabaseURL)
	if raw == "" {
		t.Skipf("set %s to a database that is not named as a test database", EnvDevDatabaseURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, config.Database{
		URL:            raw,
		MaxConns:       2,
		MinConns:       0,
		ConnectTimeout: 3 * time.Second,
	}, testsupport.Logger())
	if err != nil {
		t.Skipf("PostgreSQL unavailable at %s: %v", EnvDevDatabaseURL, err)
	}
	defer pool.Close()

	if err := testsupport.DropSchema(ctx, pool); err == nil {
		t.Fatal("DropSchema succeeded against a database not named as a test database; " +
			"the guard did not hold and `go test` can drop a real schema")
	} else if !strings.Contains(err.Error(), "refusing to drop schema") {
		t.Errorf("error = %v, want a refusal to drop the schema", err)
	}
}

// TestDropSchema_AllowsTestDatabase is the other half: the guard must not make
// the tests unable to reset their own schema.
func TestDropSchema_AllowsTestDatabase(t *testing.T) {
	pool := testsupport.Pool(t)
	if pool == nil {
		t.Skip("PostgreSQL unavailable")
	}

	if err := testsupport.DropSchema(context.Background(), pool); err != nil {
		t.Fatalf("DropSchema on a test database was refused: %v", err)
	}

	// Leave the database migrated, so test order does not matter to whoever
	// runs next.
	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("build migrator: %v", err)
	}
	if err := migrator.MigrateUp(context.Background()); err != nil {
		t.Fatalf("re-migrate after drop: %v", err)
	}
}
