package store

import (
	"path/filepath"
	"testing"
)

func TestOpenRunsMigrations(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nested", "domus.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var owner string
	if err := db.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = 'schema_owner'`).Scan(&owner); err != nil {
		t.Fatalf("query app_meta: %v", err)
	}
	if owner != "domus" {
		t.Errorf("schema_owner = %q", owner)
	}

	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d (err %v), want 1", fk, err)
	}

	// Re-running migrations is a no-op.
	if err := Migrate(ctx, db); err != nil {
		t.Errorf("second Migrate: %v", err)
	}
}
