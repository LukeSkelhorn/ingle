package store

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
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

func openTest(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "domus.db"))
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
