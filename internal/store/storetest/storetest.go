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
func Open(t testing.TB, sqlitePath string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	if base := os.Getenv(EnvPG); base != "" {
		dsn := Schema(t, base)
		db, err := store.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
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
