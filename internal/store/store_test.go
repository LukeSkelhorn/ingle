package store

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestOpenRunsMigrations(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nested", "ingle.db")

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

func openTest(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "ingle.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestEnsureHouseholdIsIdempotent(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	h1, err := st.EnsureHousehold(ctx, "Home", now)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := st.EnsureHousehold(ctx, "Other", now)
	if err != nil {
		t.Fatal(err)
	}
	if h1.ID != h2.ID || h2.Name != "Home" {
		t.Errorf("second call returned %+v, want existing %+v", h2, h1)
	}
}

func TestSchemaConstraints(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	h, err := st.EnsureHousehold(ctx, "Home", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	kid, err := st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: h.ID, Name: "Alfie", Kind: "child", Colour: "#ff8800", CreatedAt: 1})
	if err != nil {
		t.Fatalf("valid person rejected: %v", err)
	}

	badPeople := map[string]dbq.CreatePersonParams{
		"bad kind":       {HouseholdID: h.ID, Name: "A", Kind: "pet", Colour: "#ffffff", CreatedAt: 1},
		"bad colour":     {HouseholdID: h.ID, Name: "B", Kind: "adult", Colour: "red", CreatedAt: 1},
		"duplicate name": {HouseholdID: h.ID, Name: "Alfie", Kind: "adult", Colour: "#ffffff", CreatedAt: 1},
		"no household":   {HouseholdID: 999, Name: "C", Kind: "adult", Colour: "#ffffff", CreatedAt: 1},
	}
	for name, p := range badPeople {
		if _, err := st.Q.CreatePerson(ctx, p); err == nil {
			t.Errorf("%s: insert succeeded", name)
		}
	}

	chore := func(mode string, assignee *int64, startsOn string, points int64) dbq.CreateChoreParams {
		return dbq.CreateChoreParams{
			HouseholdID: h.ID, Title: "Bins", Mode: mode, AssigneeID: assignee,
			StartsOn: startsOn, Points: points, CreatedAt: 1, UpdatedAt: 1,
		}
	}
	if _, err := st.Q.CreateChore(ctx, chore("assigned", &kid.ID, "2026-11-03", 1)); err != nil {
		t.Errorf("valid chore rejected: %v", err)
	}
	badChores := map[string]dbq.CreateChoreParams{
		"assigned without assignee": chore("assigned", nil, "2026-11-03", 1),
		"pool with assignee":        chore("pool", &kid.ID, "2026-11-03", 1),
		"bad date":                  chore("pool", nil, "03/11/2026", 1),
		"negative points":           chore("pool", nil, "2026-11-03", -1),
		"bad due time": func() dbq.CreateChoreParams {
			c := chore("pool", nil, "2026-11-03", 1)
			bad := "25:00"
			c.DueTime = &bad
			return c
		}(),
	}
	for name, c := range badChores {
		if _, err := st.Q.CreateChore(ctx, c); err == nil {
			t.Errorf("%s: insert succeeded", name)
		}
	}
}

func TestMigrationsRoundTrip(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up after down: %v", err)
	}
	if _, err := st.EnsureHousehold(ctx, "Home", time.Now()); err != nil {
		t.Errorf("schema unusable after round trip: %v", err)
	}
}

// The shared chores migration rebuilds chores; occurrences and completions
// must survive it both ways, and foreign keys must be back on afterwards.
func TestSharedChoresMigrationKeepsData(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 10); err != nil {
		t.Fatalf("DownTo(10): %v", err)
	}
	for _, q := range []string{
		`INSERT INTO households (id, name, created_at) VALUES (1, 'Home', 1)`,
		`INSERT INTO people (id, household_id, name, kind, colour, created_at) VALUES (1, 1, 'Alfie', 'child', '#46A758', 1)`,
		`INSERT INTO chores (id, household_id, title, mode, assignee_id, starts_on, created_at, updated_at, day_mode) VALUES (1, 1, 'Bins', 'assigned', 1, '2026-11-02', 1, 1, 'any')`,
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on, assignee_id, status) VALUES (1, 1, 1, '2026-11-02', 1, 'done')`,
		`INSERT INTO completions (household_id, occurrence_id, person_id, completed_at, status, points, pence) VALUES (1, 1, 1, 1, 'approved', 5, 0)`,
	} {
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func() string {
		t.Helper()
		var c, o, k int
		var mode string
		if err := st.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM chores), (SELECT count(*) FROM chore_occurrences), (SELECT count(*) FROM completions), (SELECT day_mode FROM chores WHERE id = 1)`).Scan(&c, &o, &k, &mode); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d/%d/%d/%s", c, o, k, mode)
	}
	checkFK := func(when string) {
		t.Helper()
		var on int
		if err := st.DB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
			t.Errorf("%s: foreign_keys = %d (%v), want 1", when, on, err)
		}
		rows, err := st.DB.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Errorf("%s: foreign key violations", when)
		}
	}
	before := count()
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := count(); got != before {
		t.Errorf("after up = %s, want %s", got, before)
	}
	checkFK("after up")
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO chores (household_id, title, mode, starts_on, created_at, updated_at) VALUES (1, 'Tea', 'shared', '2026-11-02', 1, 1)`); err != nil {
		t.Errorf("shared chore after up: %v", err)
	}
	if _, err := p.DownTo(ctx, 10); err != nil {
		t.Fatalf("DownTo(10) again: %v", err)
	}
	checkFK("after down")
	var mode string
	if err := st.DB.QueryRowContext(ctx, `SELECT mode FROM chores WHERE title = 'Tea'`).Scan(&mode); err != nil || mode != "pool" {
		t.Errorf("shared chore after down = %q (%v), want pool", mode, err)
	}
}

// The manual points migration rebuilds ledger_entries; balances must survive
// it both ways, and 'manual' entries become plain adjustments on the way down.
func TestManualPointsMigrationKeepsLedger(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 12); err != nil {
		t.Fatalf("DownTo(12): %v", err)
	}
	for _, q := range []string{
		`INSERT INTO households (id, name, created_at) VALUES (1, 'Home', 1)`,
		`INSERT INTO people (id, household_id, name, kind, colour, created_at) VALUES (1, 1, 'Alfie', 'child', '#46A758', 1)`,
		`INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, note, created_at) VALUES (1, 1, 10, 50, 'completion', '', 1)`,
		`INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, note, created_at) VALUES (1, 1, -2, 0, 'adjustment', 'Reversed: Bins', 2)`,
	} {
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, created_at) VALUES (1, 1, -1, 0, 'manual', 3)`); err == nil {
		t.Fatal("'manual' accepted before the migration")
	}
	sum := func() string {
		t.Helper()
		var n, pts, pence int
		if err := st.DB.QueryRowContext(ctx, `SELECT count(*), sum(points), sum(pence) FROM ledger_entries`).Scan(&n, &pts, &pence); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d/%d/%d", n, pts, pence)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := sum(); got != "2/8/50" {
		t.Errorf("after up = %s, want 2/8/50", got)
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, note, created_at) VALUES (1, 1, -5, 0, 'manual', 'Hit his sister', 3)`); err != nil {
		t.Fatalf("manual after up: %v", err)
	}
	if _, err := p.DownTo(ctx, 12); err != nil {
		t.Fatalf("DownTo(12) again: %v", err)
	}
	var kinds string
	if err := st.DB.QueryRowContext(ctx, `SELECT group_concat(kind, ',') FROM (SELECT kind FROM ledger_entries ORDER BY id)`).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if got := sum(); got != "3/3/50" || kinds != "completion,adjustment,adjustment" {
		t.Errorf("after down = %s %s", got, kinds)
	}
	var on int
	if err := st.DB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Errorf("foreign_keys = %d (%v), want 1", on, err)
	}
}
