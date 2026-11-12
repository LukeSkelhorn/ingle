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
		"bad show before": func() dbq.CreateChoreParams {
			c := chore("pool", nil, "2026-11-03", 1)
			bad := int64(45)
			c.ShowBefore = &bad
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
	sub, err := fs.Sub(migrations, "migrations/sqlite")
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
	sub, err := fs.Sub(migrations, "migrations/sqlite")
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
	sub, err := fs.Sub(migrations, "migrations/sqlite")
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

// The chore times migration rebuilds chore_occurrences and moves a chore's
// time to due_times. Occurrences keep their ids (completions point at them)
// and take their chore's time; going back keeps one occurrence a day.
func TestChoreTimesMigration(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 13); err != nil {
		t.Fatalf("DownTo(13): %v", err)
	}
	exec := func(qs ...string) {
		t.Helper()
		for _, q := range qs {
			if _, err := st.DB.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	exec(
		`INSERT INTO households (id, name, created_at) VALUES (1, 'Home', 1)`,
		`INSERT INTO people (id, household_id, name, kind, colour, created_at) VALUES (1, 1, 'Alfie', 'child', '#46A758', 1)`,
		`INSERT INTO chores (id, household_id, title, mode, assignee_id, starts_on, due_time, created_at, updated_at) VALUES (1, 1, 'Teeth', 'assigned', 1, '2026-11-02', '07:30', 1, 1)`,
		`INSERT INTO chores (id, household_id, title, mode, starts_on, created_at, updated_at) VALUES (2, 1, 'Hoover', 'pool', '2026-11-02', 1, 1)`,
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on, assignee_id, status) VALUES (7, 1, 1, '2026-11-02', 1, 'done')`,
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on) VALUES (8, 1, 2, '2026-11-02')`,
		`INSERT INTO completions (id, household_id, occurrence_id, person_id, completed_at, status, points, pence) VALUES (1, 1, 7, 1, 1, 'approved', 5, 0)`,
	)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	var occs, times string
	if err := st.DB.QueryRowContext(ctx, `SELECT group_concat(id || '@' || due_time, ' ') FROM (SELECT * FROM chore_occurrences ORDER BY id)`).Scan(&occs); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, `SELECT group_concat(due_times || '/' || ifnull(show_before, 'all'), ' ') FROM (SELECT * FROM chores ORDER BY id)`).Scan(&times); err != nil {
		t.Fatal(err)
	}
	if occs != "7@07:30 8@" || times != "07:30/all /all" {
		t.Errorf("after up: occurrences %q, chores %q", occs, times)
	}

	// A second time the same day, done; then back down.
	exec(
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on, due_time, assignee_id, status) VALUES (9, 1, 1, '2026-11-02', '19:30', 1, 'done')`,
		`INSERT INTO completions (id, household_id, occurrence_id, person_id, completed_at, status, points, pence) VALUES (2, 1, 9, 1, 2, 'approved', 5, 0)`,
		`INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, completion_id, created_at) VALUES (1, 1, 5, 0, 'completion', 2, 2)`,
	)
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO chore_occurrences (household_id, chore_id, due_on, due_time) VALUES (1, 1, '2026-11-02', '19:30')`); err == nil {
		t.Error("duplicate time accepted")
	}
	if _, err := p.DownTo(ctx, 13); err != nil {
		t.Fatalf("DownTo(13) again: %v", err)
	}
	var n, linked int
	var due string
	if err := st.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM chore_occurrences), (SELECT count(*) FROM completions), (SELECT due_time FROM chores WHERE id = 1)`).Scan(&n, &linked, &due); err != nil {
		t.Fatal(err)
	}
	var points int
	if err := st.DB.QueryRowContext(ctx, `SELECT sum(points) FROM ledger_entries WHERE completion_id IS NULL`).Scan(&points); err != nil {
		t.Fatal(err)
	}
	if n != 2 || linked != 1 || due != "07:30" || points != 5 {
		t.Errorf("after down: %d occurrences, %d completions, due %q, unlinked points %d; want 2, 1, 07:30, 5", n, linked, due, points)
	}
	var on int
	if err := st.DB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Errorf("foreign_keys = %d (%v), want 1", on, err)
	}
}

func TestEachPersonMigration(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 14); err != nil {
		t.Fatalf("DownTo(14): %v", err)
	}
	exec := func(qs ...string) {
		t.Helper()
		for _, q := range qs {
			if _, err := st.DB.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	one := func(q string, dest ...any) {
		t.Helper()
		if err := st.DB.QueryRowContext(ctx, q).Scan(dest...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(
		`INSERT INTO households (id, name, created_at) VALUES (1, 'Home', 1)`,
		`INSERT INTO people (id, household_id, name, kind, colour, created_at) VALUES (1, 1, 'Alfie', 'child', '#46A758', 1), (2, 1, 'Mum', 'adult', '#4A90D9', 1)`,
		`INSERT INTO chores (id, household_id, title, mode, assignee_id, starts_on, due_times, on_the_day, created_at, updated_at) VALUES (1, 1, 'Teeth', 'assigned', 1, '2026-11-02', '07:30', 1, 1, 1)`,
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on, due_time, assignee_id, status) VALUES (7, 1, 1, '2026-11-02', '07:30', 1, 'done')`,
		`INSERT INTO completions (id, household_id, occurrence_id, person_id, completed_at, status, points, pence) VALUES (1, 1, 7, 1, 1, 'approved', 5, 0)`,
	)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	var kept string
	one(`SELECT c.title || '/' || c.due_times || '/' || c.on_the_day || '/' || c.for_everyone || '/' || c.adult_points || ' ' ||
	            o.id || '@' || o.due_time || '/' || o.for_person || '/' || o.status
	     FROM chores c JOIN chore_occurrences o ON o.chore_id = c.id`, &kept)
	if kept != "Teeth/07:30/1/0/0 7@07:30/0/done" {
		t.Errorf("after up: %q", kept)
	}

	// An Everyone chore with a copy each; a second copy for the same person
	// is refused.
	exec(
		`INSERT INTO chores (id, household_id, title, mode, starts_on, for_everyone, adult_points, points, created_at, updated_at) VALUES (2, 1, 'Bed', 'each', '2026-11-02', 1, 1, 2, 1, 1)`,
		`INSERT INTO chore_occurrences (id, household_id, chore_id, due_on, assignee_id, for_person, status) VALUES (8, 1, 2, '2026-11-02', 1, 1, 'done'), (9, 1, 2, '2026-11-02', 2, 2, 'done')`,
		`INSERT INTO completions (id, household_id, occurrence_id, person_id, completed_at, status, points, pence) VALUES (2, 1, 8, 1, 2, 'approved', 2, 0), (3, 1, 9, 2, 2, 'approved', 1, 0)`,
		`INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, completion_id, created_at) VALUES (1, 2, 1, 0, 'completion', 3, 2)`,
	)
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO chore_occurrences (household_id, chore_id, due_on, assignee_id, for_person) VALUES (1, 2, '2026-11-02', 1, 1)`); err == nil {
		t.Error("second copy for the same person accepted")
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO chores (household_id, title, mode, starts_on, created_at, updated_at) VALUES (1, 'X', 'every', '2026-11-02', 1, 1)`); err == nil {
		t.Error("unknown mode accepted")
	}

	if _, err := p.DownTo(ctx, 14); err != nil {
		t.Fatalf("DownTo(14) again: %v", err)
	}
	var mode, people string
	var occs, comps, unlinked int
	one(`SELECT mode FROM chores WHERE id = 2`, &mode)
	one(`SELECT group_concat(person_id) FROM (SELECT person_id FROM chore_people WHERE chore_id = 2 ORDER BY person_id)`, &people)
	one(`SELECT (SELECT count(*) FROM chore_occurrences WHERE chore_id = 2 AND assignee_id IS NULL), (SELECT count(*) FROM completions), (SELECT count(*) FROM ledger_entries WHERE completion_id IS NULL)`, &occs, &comps, &unlinked)
	if mode != "shared" || people != "1,2" || occs != 1 || comps != 2 || unlinked != 1 {
		t.Errorf("after down: mode %q, people %q, %d unassigned occurrences, %d completions, %d unlinked; want shared, 1,2, 1, 2, 1", mode, people, occs, comps, unlinked)
	}
	var on int
	one(`PRAGMA foreign_keys`, &on)
	if on != 1 {
		t.Errorf("foreign_keys = %d, want 1", on)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up again: %v", err)
	}
}

// v0.3's migrations (modules, bank requests, bank balances) come after
// v0.2.4's 14 and 15, so a database upgraded from v0.2.4 runs just them.
func TestV03MigrationsAfterV024(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	sub, err := fs.Sub(migrations, "migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.DB, sub)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"household_modules", "transfer_requests", "person_bank"}
	exists := func(name string) bool {
		var n int
		if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	// Back to where v0.2.4 leaves a database.
	if _, err := p.DownTo(ctx, 15); err != nil {
		t.Fatalf("DownTo(15): %v", err)
	}
	for _, name := range tables {
		if exists(name) {
			t.Errorf("%s exists at v0.2.4", name)
		}
	}
	results, err := p.UpTo(ctx, 18) // v0.3's last; later ones are tested on their own
	if err != nil {
		t.Fatalf("Up from v0.2.4: %v", err)
	}
	var ran []int64
	for _, r := range results {
		ran = append(ran, r.Source.Version)
	}
	if fmt.Sprint(ran) != "[16 17 18]" {
		t.Errorf("upgrade ran %v, want [16 17 18]", ran)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 18 {
		t.Errorf("version = %d, %v; want 18", v, err)
	}
	for _, name := range tables {
		if !exists(name) {
			t.Errorf("%s missing after upgrade", name)
		}
	}
	// main's columns are still there alongside.
	for _, q := range []string{
		`SELECT for_everyone, adult_points, due_times, show_before FROM chores LIMIT 0`,
		`SELECT due_time FROM chore_occurrences LIMIT 0`,
	} {
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}
