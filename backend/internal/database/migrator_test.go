package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/testsupport"
)

func TestMigrator_AppliesOnceAndIsIdempotent(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}

	// MigrateUp runs twice: the second call must be a no-op rather than an error
	// or a re-application.
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("first MigrateUp() error = %v", err)
	}
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("second MigrateUp() error = %v, want nil (idempotent)", err)
	}

	statuses, err := migrator.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("Status() returned nothing")
	}
	for _, status := range statuses {
		if !status.Applied {
			t.Errorf("migration %d_%s is not applied after MigrateUp", status.Version, status.Name)
		}
		if status.AppliedAt == "" {
			t.Errorf("migration %d_%s has no applied timestamp", status.Version, status.Name)
		}
	}
}

func TestMigrator_DownThenUpRoundTrip(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	if err := migrator.MigrateDown(ctx); err != nil {
		t.Fatalf("MigrateDown() error = %v", err)
	}

	// The rollback must actually remove the schema, not just the bookkeeping.
	var usersExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = 'users')`).Scan(&usersExists)
	if err != nil {
		t.Fatalf("check users table: %v", err)
	}
	if usersExists {
		t.Error("users table survived MigrateDown")
	}

	statuses, err := migrator.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	for _, status := range statuses {
		if status.Applied {
			t.Errorf("migration %d is still marked applied after MigrateDown", status.Version)
		}
	}

	// Re-applying must work, which proves the down script is symmetric.
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("re-apply after down error = %v", err)
	}
}

func TestMigrator_DownWithNothingAppliedReportsClearly(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}

	// Roll back everything first.
	for {
		err := migrator.MigrateDown(ctx)
		if errors.Is(err, database.ErrNoMigrationToRollBack) {
			break
		}
		if err != nil {
			t.Fatalf("MigrateDown() error = %v", err)
		}
	}

	if err := migrator.MigrateDown(ctx); !errors.Is(err, database.ErrNoMigrationToRollBack) {
		t.Errorf("MigrateDown() error = %v, want ErrNoMigrationToRollBack", err)
	}

	// Leave the database usable for whatever runs next.
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("cleanup MigrateUp() error = %v", err)
	}
}

func TestMigrator_ResetReappliesFromScratch(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}

	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if err := migrator.MigrateReset(ctx); err != nil {
		t.Fatalf("MigrateReset() error = %v", err)
	}

	statuses, err := migrator.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	for _, status := range statuses {
		if !status.Applied {
			t.Errorf("migration %d not applied after reset", status.Version)
		}
	}
}

func TestInTx_RollsBackOnError(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	sentinel := errors.New("business rule failed")

	err := pool.InTx(ctx, func(tx database.Execer) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO users (email, password_hash, name) VALUES ('rollback@example.com', 'x', 'N')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() error = %v, want the callback's error", err)
	}

	// The insert must have been rolled back with the rest of the transaction.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE email = 'rollback@example.com'`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Errorf("row survived a rolled back transaction, count = %d", count)
	}
}

func TestInTx_CommitsOnSuccess(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	err := pool.InTx(ctx, func(tx database.Execer) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO users (email, password_hash, name) VALUES ('commit@example.com', 'x', 'N')`)
		return err
	})
	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE email = 'commit@example.com'`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("committed row not found, count = %d, want 1", count)
	}

	// Clean up so a later test does not see this row.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE email = 'commit@example.com'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestPool_HealthCheck(t *testing.T) {
	pool := testsupport.Pool(t)
	if err := pool.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck() error = %v, want nil against a live database", err)
	}
}

func TestLoadMigrations_FromThePublicAPI(t *testing.T) {
	all, err := database.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("LoadMigrations() returned nothing")
	}
}
