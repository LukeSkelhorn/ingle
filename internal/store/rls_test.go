package store_test

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
)

func needPG(t *testing.T) {
	t.Helper()
	if !storetest.Postgres() {
		t.Skip("set " + storetest.EnvPG + " (task test:pg) to test row-level security")
	}
}

// tables lists the app's tables (not goose's).
func tables(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(store.AsSystem(ctx), `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relname <> 'goose_db_version'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if len(out) < 20 {
		t.Fatalf("only %d tables: %v", len(out), out)
	}
	return out
}

// TestRowLevelSecurityCoverage fails if a table has no row-level security,
// isn't FORCEd (so its owner would skip it) or has no policy: add the table
// to migrations/postgres's policies.
func TestRowLevelSecurityCoverage(t *testing.T) {
	needPG(t)
	ctx := t.Context()
	db := storetest.OpenScoped(t, "")
	rows, err := db.QueryContext(store.AsSystem(ctx), `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       (SELECT count(*) FROM pg_policy p WHERE p.polrelid = c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relname <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var rls, force bool
		var policies int
		if err := rows.Scan(&name, &rls, &force, &policies); err != nil {
			t.Fatal(err)
		}
		n++
		if !rls || !force || policies == 0 {
			t.Errorf("%s: row level security %v, forced %v, %d policies; want on, forced, at least one", name, rls, force, policies)
		}
	}
	if n < 20 {
		t.Errorf("checked only %d tables", n)
	}
}

// TestRowLevelSecurityKeepsHouseholdsApart runs SQL that forgets its
// WHERE household_id = ... as household A, connected as the app's role,
// and gets none of household B's rows.
func TestRowLevelSecurityKeepsHouseholdsApart(t *testing.T) {
	needPG(t)
	ctx := t.Context()
	db := storetest.OpenScoped(t, "")
	st := store.New(db)
	if bypass, err := store.CanBypassRLS(ctx, db); err != nil || bypass {
		t.Fatalf("app role bypasses row-level security: %v, %v", bypass, err)
	}

	now := time.Now()
	a := seed(t, st, "A", now)
	b := seed(t, st, "B", now)
	actx, bctx := store.WithHousehold(ctx, a), store.WithHousehold(ctx, b)

	// Every table, read with no WHERE at all: only A's rows, or none.
	for _, table := range tables(t, ctx, db) {
		col := "household_id"
		switch table {
		case "households":
			col = "id"
		case "app_meta":
			continue
		}
		var other int
		if err := st.SQL().QueryRowContext(actx, "SELECT count(*) FROM "+table+" WHERE "+col+" <> ?", a).Scan(&other); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if other != 0 {
			t.Errorf("%s: household A sees %d of another household's rows", table, other)
		}
	}
	var people int
	if err := st.SQL().QueryRowContext(actx, "SELECT count(*) FROM people").Scan(&people); err != nil || people != 1 {
		t.Errorf("A sees %d people (%v), want just its own 1", people, err)
	}

	// Writing B's rows as A is refused, and so is changing them.
	_, err := st.Q.CreatePerson(actx, dbq.CreatePersonParams{
		HouseholdID: b, Name: "Intruder", Kind: "child", Colour: "#000000", CreatedAt: now.Unix(),
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("A creating a person in B: err = %v, want a row-level security error", err)
	}
	res, err := st.SQL().ExecContext(actx, "UPDATE people SET name = 'Changed' WHERE household_id = ?", b)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("A renamed %d of B's people", n)
	}
	if p, err := st.Q.ListPeople(bctx, b); err != nil || len(p) != 1 || p[0].Name != "Kid B" {
		t.Errorf("B's people = %v, %v", p, err)
	}

	// The same holds inside a transaction.
	err = st.WithTx(actx, func(q *dbq.Queries) error {
		p, err := q.ListPeople(actx, b)
		if len(p) != 0 {
			t.Errorf("A lists %d of B's people in a transaction", len(p))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// No household: refused before it reaches the database.
	if _, err := st.Q.ListPeople(ctx, a); !errors.Is(err, store.ErrNoHousehold) {
		t.Errorf("no household: err = %v, want ErrNoHousehold", err)
	}
	// A system lookup may read people, but not change them.
	sys := store.AsSystem(ctx)
	if p, err := st.Q.ListPeople(sys, b); err != nil || len(p) != 1 {
		t.Errorf("system lookup of B's people = %d, %v", len(p), err)
	}
	res, err = st.SQL().ExecContext(sys, "UPDATE people SET name = 'Changed'")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("a system lookup changed %d people", n)
	}
	if _, err := st.Q.ListChores(sys, a); err != nil {
		t.Fatal(err)
	} else if c, _ := st.Q.ListChores(sys, a); len(c) != 0 {
		t.Errorf("a system lookup read %d chores, want none", len(c))
	}
}

// seed makes a household with one child and one chore.
func seed(t *testing.T, st *store.Store, name string, now time.Time) int64 {
	t.Helper()
	h, err := st.Q.CreateHousehold(store.AsSystem(t.Context()), dbq.CreateHouseholdParams{Name: name, CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithHousehold(t.Context(), h.ID)
	p, err := st.Q.CreatePerson(ctx, dbq.CreatePersonParams{
		HouseholdID: h.ID, Name: "Kid " + name, Kind: "child", Colour: "#46A758", CreatedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SQL().ExecContext(ctx, `INSERT INTO chores (household_id, title, mode, assignee_id, starts_on, created_at, updated_at)
		VALUES (?, 'Chore', 'assigned', ?, '2026-11-12', ?, ?)`, h.ID, p.ID, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	return h.ID
}

// TestAsSystemCallers keeps AsSystem to the lookups made before the
// household is known. A new caller means a query that can read any
// household's households, displays, sessions or people: check it really
// has to, then add it here.
func TestAsSystemCallers(t *testing.T) {
	want := map[string]int{
		"app/app.go:build":                        1, // list households for the jobs
		"app/app.go:eachHousehold":                1, // list households for the jobs
		"internal/auth/auth.go:Display":           1, // display by token
		"internal/auth/auth.go:SignIn":            1, // adult by email
		"internal/auth/auth.go:Session":           1, // session by token
		"internal/auth/auth.go:EndSession":        1, // session by token
		"internal/server/modules.go:households":   1, // list households for the module loops
		"internal/store/store.go:EnsureHousehold": 1, // self-hosted's one household
		"internal/store/store.go:CanBypassRLS":    1, // reads pg_roles
	}
	root := filepath.Join("..", "..")
	got := map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "storetest") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(src), "AsSystem(") {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				if name == "AsSystem" {
					got[filepath.ToSlash(rel)+":"+fn.Name.Name]++
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if want[k] != got[k] {
			t.Errorf("%s calls AsSystem %d times, want %d", k, got[k], want[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if _, ok := got[k]; !ok {
			t.Errorf("%s no longer calls AsSystem: remove it from the list", k)
		}
	}
}
