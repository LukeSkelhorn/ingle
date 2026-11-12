package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
)

// TestSchemaParity checks that the Postgres schema has the same tables and
// columns, in the same order and of matching types, as SQLite's. sqlc
// generates the queries from the SQLite schema, so a migration added for one
// engine but not the other fails here. It runs with task test:pg.
func TestSchemaParity(t *testing.T) {
	base := os.Getenv(storetest.EnvPG)
	if base == "" {
		t.Skip("set " + storetest.EnvPG + " (task test:pg) to compare with Postgres")
	}
	ctx := t.Context()
	lite, err := store.Open(ctx, filepath.Join(t.TempDir(), "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()
	pg, err := store.OpenPostgres(ctx, store.PostgresOptions{URL: storetest.Schema(t, base), AllowUnscoped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	want := sqliteColumns(t, ctx, lite)
	got := postgresColumns(t, ctx, pg)
	for _, table := range slices.Sorted(keys(want, got)) {
		if w, g := want[table], got[table]; !slices.Equal(w, g) {
			t.Errorf("%s:\n sqlite   %v\n postgres %v", table, w, g)
		}
	}
}

func keys(ms ...map[string][]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		seen := map[string]bool{}
		for _, m := range ms {
			for k := range m {
				if !seen[k] {
					seen[k] = true
					if !yield(k) {
						return
					}
				}
			}
		}
	}
}

// Each column is "name:kind", kind being int, text or blob.
func sqliteColumns(t *testing.T, ctx context.Context, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT m.name, p.name, p.type FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name <> 'goose_db_version'
		ORDER BY m.name, p.cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := map[string]string{"INTEGER": "int", "TEXT": "text", "BLOB": "blob"}
	out := map[string][]string{}
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			t.Fatal(err)
		}
		out[table] = append(out[table], fmt.Sprintf("%s:%s", col, kinds[strings.ToUpper(typ)]))
	}
	return out
}

func postgresColumns(t *testing.T, ctx context.Context, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name <> 'goose_db_version'
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := map[string]string{"bigint": "int", "text": "text", "bytea": "blob"}
	out := map[string][]string{}
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			t.Fatal(err)
		}
		k, ok := kinds[typ]
		if !ok {
			k = "?" + typ
		}
		out[table] = append(out[table], fmt.Sprintf("%s:%s", col, k))
	}
	return out
}
