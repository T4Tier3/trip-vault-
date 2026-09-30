package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tripvault/backend/internal/config"
)

// Execer is the subset of pgx shared by pgxpool.Pool, pgx.Tx and pgxpool.Tx.
// The migrator and repositories are written against it so a repository method
// can run on the pool or inside a caller's transaction without change, and so
// tests can supply a stub.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxRunner is an Execer that can also run a function inside a transaction.
type TxRunner interface {
	Execer
	InTx(ctx context.Context, fn func(tx Execer) error) error
}

// ErrNoRows is returned by QueryRow when a query matched nothing. It is
// re-exported so repositories can compare against a sentinel from this package
// instead of importing pgx directly.
var ErrNoRows = pgx.ErrNoRows

// Pool wraps pgxpool.Pool with the pieces the application actually needs.
type Pool struct {
	*pgxpool.Pool
}

// NewPool dials PostgreSQL and verifies the connection before returning.
func NewPool(ctx context.Context, cfg config.Database, log *slog.Logger) (*Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// QueryExecModeDescribeExec, not CacheStatement.
	//
	// CacheStatement is the pgx default and the fastest option, but it assumes
	// the schema never changes under a live connection. When a migration
	// recreates an object, the cached plan's result type no longer matches and
	// PostgreSQL refuses the query:
	//
	//   ERROR: cached plan must not change result type (SQLSTATE 40574)
	//
	// That is not a theoretical window. It happens on every rolling deploy,
	// where a migration drops and recreates tables while the previous version is
	// still serving traffic, and it turns an endpoint into a 500 until the
	// process restarts. DescribeExec re-describes a statement whose plan has
	// gone stale instead of failing.
	//
	// The cost is one extra round trip per query, which is why it is not the
	// default here by reflex: this service is not read-heavy enough to trade
	// correctness for it. If query volume ever justifies the trade, the
	// alternative is CacheStatement plus a deploy that drains connections before
	// migrating. QueryExecModeExec would avoid the extra round trip but rejects
	// the purpose enum passed as a plain Go string, so it is not an option.
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec

	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	// Fail fast and loudly: a server that accepted the socket but cannot serve
	// queries is not usable, and discovering that on first request is worse.
	pingCtx, pingCancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if log != nil {
		log.Info("postgres connected",
			"host", poolCfg.ConnConfig.Host,
			"port", poolCfg.ConnConfig.Port,
			"database", poolCfg.ConnConfig.Database,
			"max_conns", poolCfg.MaxConns,
		)
	}

	return &Pool{Pool: pool}, nil
}

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic. The transaction is passed to fn as an Execer, so the same
// repository calls can be enlisted atomically.
func (p *Pool) InTx(ctx context.Context, fn func(tx Execer) error) (err error) {
	tx, beginErr := p.Begin(ctx)
	if beginErr != nil {
		return fmt.Errorf("begin transaction: %w", beginErr)
	}

	// Rollback after a successful commit is a no-op, so this is safe to defer
	// unconditionally.
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(r)
		}
		if err != nil {
			// Use a context detached from the caller's: the caller's may already
			// be cancelled, which would leave the connection holding the
			// transaction open until it is reaped.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// HealthCheck reports whether PostgreSQL is reachable, for the readiness probe.
func (p *Pool) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	return nil
}

// IsUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation, optionally scoped to a specific constraint name.
//
// Repositories use this instead of inspecting driver error text, so behaviour
// does not depend on pgx internals.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// formatTimestamp renders a timestamp value read from the database.
func formatTimestamp(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
