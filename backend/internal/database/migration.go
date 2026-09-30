package database

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/tripvault/backend/migrations"
)

// Migration is a single reversible schema change.
type Migration struct {
	// Version is the numeric prefix of the filename, e.g. 1 for "0001_init.up.sql".
	// Versions must be unique and are applied in ascending order.
	Version int64
	// Name is the human readable part of the filename, e.g. "init".
	Name string
	// Up and Down hold the SQL for each direction.
	Up   string
	Down string
}

// Filename suffixes the runner expects. A migration must provide both halves.
const (
	upSuffix   = ".up.sql"
	downSuffix = ".down.sql"
)

// LoadMigrations reads and validates every migration from the embedded
// filesystem. A missing or mismatched up/down pair is a load-time error, so a
// broken migration can never reach a database.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	// Collect both halves keyed by version so mismatches surface as errors
	// rather than as half-applied schema changes.
	ups := map[int64]string{}
	downs := map[int64]string{}
	names := map[int64]string{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()

		var (
			isUp  = strings.HasSuffix(filename, upSuffix)
			hasIt = strings.HasSuffix(filename, upSuffix) || strings.HasSuffix(filename, downSuffix)
		)
		if !hasIt {
			// Anything that is not a migration (docs, notes) is ignored.
			continue
		}

		stem := strings.TrimSuffix(strings.TrimSuffix(filename, upSuffix), downSuffix)
		version, name, ok := splitMigrationName(stem)
		if !ok {
			return nil, fmt.Errorf("migration %q does not match NNNN_name%s naming", filename, upSuffix)
		}

		// An up/down pair legitimately shares a version, so uniqueness is
		// checked per direction rather than across both.
		if isUp {
			if previous, dup := ups[version]; dup {
				return nil, fmt.Errorf("duplicate migration version %d: %q and %q", version, previous, filename)
			}
			names[version] = name
		} else if previous, dup := downs[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", version, previous, filename)
		}

		body, err := migrations.FS.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", filename, err)
		}
		if isUp {
			ups[version] = string(body)
		} else {
			downs[version] = string(body)
		}
	}

	if len(names) == 0 {
		return nil, errors.New("no migrations found")
	}

	out := make([]Migration, 0, len(names))
	for version, name := range names {
		up, hasUp := ups[version]
		down, hasDown := downs[version]
		switch {
		case !hasUp:
			return nil, fmt.Errorf("migration %d_%s is missing its %s file", version, name, upSuffix)
		case !hasDown:
			return nil, fmt.Errorf("migration %d_%s is missing its %s file", version, name, downSuffix)
		}
		out = append(out, Migration{Version: version, Name: name, Up: up, Down: down})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// splitMigrationName parses "0001_init" into version 1 and name "init".
func splitMigrationName(stem string) (version int64, name string, ok bool) {
	idx := strings.Index(stem, "_")
	if idx <= 0 || idx == len(stem)-1 {
		return 0, "", false
	}
	version, err := strconv.ParseInt(stem[:idx], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return version, stem[idx+1:], true
}
