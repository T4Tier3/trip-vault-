package database_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/testsupport"
)

// TestPool_SurvivesSchemaRecreation is a regression test.
//
// The pool used to run queries in QueryExecModeCacheStatement, which is pgx's
// default and its fastest mode. It assumes the schema never changes under a
// live connection. When a migration recreates an object that a cached plan
// depends on, PostgreSQL rejects the query:
//
//	ERROR:  cached plan must not change result type  (SQLSTATE 40574)
//
// The first version of this test run reproduced it by accident: a `go test`
// invocation shares DATABASE_URL with the dev server, and testsupport's repair
// path drops and recreates the public schema. Every endpoint that touched
// auth_tokens then answered 500 against a perfectly healthy database.
//
// The realistic trigger is a rolling deploy, where a migration recreates tables
// while the previous version still serves traffic. A plan invalidated by
// someone else's migration must not surface to a user as a server error.
func TestPool_SurvivesSchemaRecreation(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	userID := uuid.New()
	email := fmt.Sprintf("recreate-%s@example.com", testsupport.NewID())
	seed := func() {
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (id, email, password_hash, name, email_verified) VALUES ($1,$2,'hash','Recreate',true)`,
			userID, email); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	seed()

	// The query under test: an UPDATE ... RETURNING, which is the shape that
	// trips the result-type check.
	const query = `
		UPDATE auth_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND purpose = $2
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id`

	tokenHash := strings.Repeat("a", 64)
	consume := func(label string) {
		t.Helper()
		var out uuid.UUID
		err := pool.QueryRow(ctx, query, tokenHash, "email_verification").Scan(&out)
		// No rows is the expected answer: the token does not exist. The point is
		// that the failure mode is "no rows" and not SQLSTATE 40574.
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("%s: query failed after schema change: %v", label, err)
		}
	}

	consume("before schema change")

	// Recreate the schema underneath the live pool, which is what a migration
	// rolling out under a running server does.
	if err := testsupport.DropSchema(ctx, pool); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	migrator, err := database.NewMigrator(pool, testsupport.Logger())
	if err != nil {
		t.Fatalf("build migrator: %v", err)
	}
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	// The user row is gone with the old schema, so seed a new one to prove the
	// table is usable and not merely that the statement was re-prepared.
	seed()

	consume("after schema change")
	consume("after schema change, second run")
}

// TestPool_UsesDescribeExec pins the exec mode so a future change back to
// pgx's faster default does not silently reintroduce the bug above.
func TestPool_UsesDescribeExec(t *testing.T) {
	pool := testsupport.Pool(t)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	if got := conn.Conn().Config().DefaultQueryExecMode; got != pgx.QueryExecModeDescribeExec {
		t.Errorf("DefaultQueryExecMode = %v, want %v; CacheStatement fails with SQLSTATE 40574 "+
			"after a migration changes a cached plan's result type",
			got, pgx.QueryExecModeDescribeExec)
	}
}
