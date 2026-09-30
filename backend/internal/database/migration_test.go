package database

import (
	"strings"
	"testing"
)

func TestLoadMigrations_EmbeddedFilesLoadAndAreOrdered(t *testing.T) {
	got, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v, want nil", err)
	}

	if len(got) == 0 {
		t.Fatal("LoadMigrations() returned no migrations, want at least 0001_init")
	}

	for i := 1; i < len(got); i++ {
		if got[i].Version <= got[i-1].Version {
			t.Errorf("migrations not ordered by version: %d after %d", got[i].Version, got[i-1].Version)
		}
	}

	first := got[0]
	if first.Version != 1 {
		t.Errorf("first version = %d, want 1", first.Version)
	}
	if first.Name != "init" {
		t.Errorf("first name = %q, want %q", first.Name, "init")
	}
	if strings.TrimSpace(first.Up) == "" {
		t.Error("first migration has empty Up SQL")
	}
	if strings.TrimSpace(first.Down) == "" {
		t.Error("first migration has empty Down SQL")
	}
}

func TestLoadMigrations_InitCreatesAuthTables(t *testing.T) {
	all, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	up := all[0].Up

	for _, table := range []string{"users", "auth_tokens", "refresh_tokens", "devices"} {
		if !strings.Contains(up, "CREATE TABLE "+table) {
			t.Errorf("migration does not create table %q", table)
		}
	}
}

func TestLoadMigrations_DoesNotStoreMediaInPostgres(t *testing.T) {
	all, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	up := strings.ToUpper(all[0].Up)

	// Binary media belongs in ImageKit. Bytea columns would mean large objects
	// in PostgreSQL, which the architecture explicitly rules out.
	for _, forbidden := range []string{"BYTEA", "BLOB"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration contains %s, binary media must not live in PostgreSQL", forbidden)
		}
	}
}

func TestLoadMigrations_TokensAreStoredHashed(t *testing.T) {
	all, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	up := all[0].Up

	// A plaintext token column would be a credential store, not a hash store.
	if !strings.Contains(up, "token_hash") {
		t.Error("auth/refresh token tables have no *_hash column")
	}
}

func TestSplitMigrationName(t *testing.T) {
	tests := []struct {
		stem       string
		wantVer    int64
		wantName   string
		wantParsed bool
	}{
		{"0001_init", 1, "init", true},
		{"0002_add_trips", 2, "add_trips", true},
		{"12_short", 12, "short", true},
		{"init", 0, "", false},
		{"0001_", 0, "", false},
		{"_init", 0, "", false},
		{"abc_init", 0, "", false},
		{"", 0, "", false},
	}

	for _, tt := range tests {
		version, name, ok := splitMigrationName(tt.stem)
		if ok != tt.wantParsed {
			t.Errorf("splitMigrationName(%q) parsed = %v, want %v", tt.stem, ok, tt.wantParsed)
			continue
		}
		if ok && (version != tt.wantVer || name != tt.wantName) {
			t.Errorf("splitMigrationName(%q) = (%d, %q), want (%d, %q)", tt.stem, version, name, tt.wantVer, tt.wantName)
		}
	}
}
