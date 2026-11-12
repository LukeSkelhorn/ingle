// Package storetest opens a migrated database for tests.
//
// Tests use SQLite by default. With INGLE_TEST_PG_URL set (task test:pg),
// each test gets its own empty Postgres schema instead, so the same tests
// run on both engines.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store"
)

// EnvPG names the Postgres server tests run against, when set.
const EnvPG = "INGLE_TEST_PG_URL"

// Postgres reports whether tests are running on Postgres.
func Postgres() bool { return os.Getenv(EnvPG) != "" }

// Open returns a freshly migrated database, closed when the test ends.
// sqlitePath is where the SQLite file goes ("" for a temp dir); it is
// ignored on Postgres.
//
// On Postgres it connects as the server's superuser, which skips row-level
// security, and lets statements run without a household in their context.
// It checks the SQL works on Postgres. OpenScoped checks the scoping.
func Open(t testing.TB, sqlitePath string) *sql.DB {
	t.Helper()
	if base := os.Getenv(EnvPG); base != "" {
		return openPG(t, store.PostgresOptions{URL: Schema(t, base), AllowUnscoped: true})
	}
	return openSQLite(t, sqlitePath)
}

// AppRole is the role OpenScoped connects as. It isn't a superuser, can't
// bypass row-level security and doesn't own the tables, like the app's own
// role in production.
const AppRole = "ingle_app_test"

// OpenScoped is Open, but on Postgres it connects as AppRole, as the app
// does in production: row-level security applies, and a statement without
// a household (or store.AsSystem) in its context fails.
func OpenScoped(t testing.TB, sqlitePath string) *sql.DB {
	t.Helper()
	base := os.Getenv(EnvPG)
	if base == "" {
		return openSQLite(t, sqlitePath)
	}
	owner := Schema(t, base)
	ensureAppRole(t, base)
	u, err := url.Parse(owner)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(AppRole, AppRole)
	return openPG(t, store.PostgresOptions{URL: u.String(), MigrateURL: owner})
}

func ensureAppRole(t testing.TB, base string) {
	t.Helper()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// Packages run in parallel, so two may try to create it at once.
	_, err = admin.ExecContext(context.Background(), `DO $$ BEGIN
		CREATE ROLE `+AppRole+` LOGIN PASSWORD '`+AppRole+`' NOSUPERUSER NOBYPASSRLS;
	EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
	END $$`)
	if err != nil {
		t.Fatalf("create %s: %v", AppRole, err)
	}
}

func openPG(t testing.TB, o store.PostgresOptions) *sql.DB {
	t.Helper()
	db, err := store.OpenPostgres(context.Background(), o)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openSQLite(t testing.TB, sqlitePath string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	if sqlitePath == "" {
		sqlitePath = filepath.Join(t.TempDir(), "ingle.db")
	}
	db, err := store.Open(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Schema creates an empty schema on the Postgres server at base, dropped
// when the test ends, and returns a URL whose connections use it.
func Schema(t testing.TB, base string) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "t_" + hex.EncodeToString(b)

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+name); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		a, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.ExecContext(context.Background(), `DROP SCHEMA `+name+` CASCADE`)
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvPG, err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}
