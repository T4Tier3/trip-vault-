package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Migrator applies and rolls back schema migrations, tracking what it has run
// in a schema_migrations table.
type Migrator struct {
	conn       TxRunner
	migrations []Migration
	log        *slog.Logger
}

// NewMigrator builds a Migrator over conn using the embedded migrations.
func NewMigrator(conn TxRunner, log *slog.Logger) (*Migrator, error) {
	all, err := LoadMigrations()
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Migrator{conn: conn, migrations: all, log: log}, nil
}

// MigrationStatus describes the applied state of one migration.
type MigrationStatus struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt string
}

// ErrNoMigrationToRollBack is returned when Down is called with nothing applied.
var ErrNoMigrationToRollBack = errors.New("no migration to roll back")

// MigrateUp applies every pending migration in ascending version order. Each
// migration runs in its own transaction together with the bookkeeping insert,
// so a failure leaves the schema exactly as it was.
func (m *Migrator) MigrateUp(ctx context.Context) error {
	if err := m.ensureTable(ctx); err != nil {
		return err
	}

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, mig := range m.migrations {
		if _, done := applied[mig.Version]; done {
			continue
		}
		if err := m.runOne(ctx, mig, true); err != nil {
			return fmt.Errorf("apply migration %d_%s: %w", mig.Version, mig.Name, err)
		}
		m.log.Info("migration applied", "version", mig.Version, "name", mig.Name)
	}
	return nil
}

// MigrateDown rolls back the most recently applied migration. Rolling back one
// step at a time keeps a mistake easy to undo.
func (m *Migrator) MigrateDown(ctx context.Context) error {
	if err := m.ensureTable(ctx); err != nil {
		return err
	}

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	// Highest applied version wins.
	var latest *Migration
	for i := range m.migrations {
		mig := &m.migrations[i]
		if _, done := applied[mig.Version]; done {
			if latest == nil || mig.Version > latest.Version {
				latest = mig
			}
		}
	}
	if latest == nil {
		return ErrNoMigrationToRollBack
	}

	if err := m.runOne(ctx, *latest, false); err != nil {
		return fmt.Errorf("roll back migration %d_%s: %w", latest.Version, latest.Name, err)
	}
	m.log.Info("migration rolled back", "version", latest.Version, "name", latest.Name)
	return nil
}

// MigrateReset rolls back every applied migration and then re-applies them from
// scratch. This is destructive and only intended for local development.
//
// Rolling back through the down scripts, rather than dropping the bookkeeping
// table alone, is what makes the result correct: dropping bookkeeping without
// dropping the schema would leave the old tables in place and the re-apply would
// fail on the first existing relation.
func (m *Migrator) MigrateReset(ctx context.Context) error {
	for {
		err := m.MigrateDown(ctx)
		if errors.Is(err, ErrNoMigrationToRollBack) {
			break
		}
		if err != nil {
			return err
		}
	}

	// ensureTable recreated the bookkeeping table on the first MigrateDown call.
	if _, err := m.conn.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations`); err != nil {
		return fmt.Errorf("drop schema_migrations: %w", err)
	}

	m.log.Warn("schema rolled back, re-applying from scratch")
	return m.MigrateUp(ctx)
}

// Status returns the applied state of every known migration.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}

	rows, err := m.conn.Query(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	stamps := map[int64]string{}
	for rows.Next() {
		var (
			version int64
			at      any
		)
		if err := rows.Scan(&version, &at); err != nil {
			return nil, fmt.Errorf("scan schema_migrations row: %w", err)
		}
		stamps[version] = formatTimestamp(at)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]MigrationStatus, 0, len(m.migrations))
	for _, mig := range m.migrations {
		at, applied := stamps[mig.Version]
		out = append(out, MigrationStatus{
			Version:   mig.Version,
			Name:      mig.Name,
			Applied:   applied,
			AppliedAt: at,
		})
	}
	return out, nil
}

// runOne executes a single migration and records or clears its bookkeeping row
// inside the same transaction as the schema change.
func (m *Migrator) runOne(ctx context.Context, mig Migration, up bool) error {
	sql := mig.Up
	bookkeeping := `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`
	args := []any{mig.Version, mig.Name}
	if !up {
		sql = mig.Down
		bookkeeping = `DELETE FROM schema_migrations WHERE version = $1`
		// The delete is keyed on version alone, so it must be given one argument
		// rather than the pair the insert takes.
		args = []any{mig.Version}
	}

	return m.conn.InTx(ctx, func(tx Execer) error {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, bookkeeping, args...); err != nil {
			return err
		}
		return nil
	})
}

// ensureTable creates the bookkeeping table if it is missing.
func (m *Migrator) ensureTable(ctx context.Context) error {
	_, err := m.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT      PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

// appliedVersions returns the set of versions already applied.
func (m *Migrator) appliedVersions(ctx context.Context) (map[int64]struct{}, error) {
	rows, err := m.conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	out := map[int64]struct{}{}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied version: %w", err)
		}
		out[version] = struct{}{}
	}
	return out, rows.Err()
}
