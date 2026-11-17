package chores

import (
	"errors"
	"testing"
	"time"
)

func TestDoFor(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.adultChore("Make bed", Input{Mode: ModeAssigned, AssigneeID: &f.kid.ID})
	occ := f.onlyOccurrence()

	c, err := f.svc.DoFor(ctx, f.hh, occ.ID, f.kid.ID, f.adult.ID)
	if err != nil {
		t.Fatalf("DoFor: %v", err)
	}
	if c.PersonID != f.adult.ID || c.ForPersonID == nil || *c.ForPersonID != f.kid.ID {
		t.Errorf("completion person %d for %v, want %d for %d", c.PersonID, c.ForPersonID, f.adult.ID, f.kid.ID)
	}
	if c.Status != StatusApproved || c.Points != 3 || c.Pence != 0 {
		t.Errorf("completion = %s, %d pts, %dp; want approved, 3 pts, 0p", c.Status, c.Points, c.Pence)
	}
	if b := f.balance(f.adult); b.Points != 3 || b.Pence != 0 {
		t.Errorf("adult balance = %+v, want 3 points", b)
	}
	if b := f.balance(f.kid); b.Points != 0 || b.Pence != 0 {
		t.Errorf("child balance = %+v, want nothing", b)
	}

	col := f.boardAt("2026-11-02").column("Alfie")
	if col.DoneToday != 1 || col.TotalToday != 1 {
		t.Errorf("Alfie's column %d of %d done, want 1 of 1", col.DoneToday, col.TotalToday)
	}
	if len(col.Cards) != 1 || !col.Cards[0].Done || col.Cards[0].DoneBy != "Mum" {
		t.Errorf("Alfie's cards = %+v, want Make bed done by Mum", col.Cards)
	}
	if mum := f.boardAt("2026-11-02").column("Mum"); len(mum.Cards) != 0 {
		t.Errorf("Mum's column has %d cards, want none", len(mum.Cards))
	}

	if _, err := f.svc.DoFor(ctx, f.hh, occ.ID, f.kid.ID, f.adult.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("DoFor again = %v, want ErrNotOpen", err)
	}

	got, err := f.svc.Completions(ctx, f.hh)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Person.Name != "Mum" || got[0].ForName != "Alfie" {
		t.Errorf("completions = %+v, want Mum for Alfie", got)
	}
	hist, _, err := f.svc.History(ctx, f.hh, f.adult.ID, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].For != "Alfie" || hist[0].Points != 3 {
		t.Errorf("Mum's history = %+v, want 3 pts for Alfie", hist)
	}

	if err := f.svc.Undo(ctx, f.hh, c.ID); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if b := f.balance(f.adult); b.Points != 0 {
		t.Errorf("adult balance after undo = %+v, want 0", b)
	}
	if col := f.boardAt("2026-11-02").column("Alfie"); col.DoneToday != 0 || len(col.Cards) != 1 {
		t.Errorf("after undo Alfie's column %d done of %d cards, want 0 of 1", col.DoneToday, len(col.Cards))
	}
}

func TestDoForSkipsApproval(t *testing.T) {
	f := newFixture(t)
	f.adultChore("Feed cat", Input{Mode: ModeAssigned, AssigneeID: &f.kid.ID, RequiresApproval: true})
	occ := f.onlyOccurrence()
	c, err := f.svc.DoFor(t.Context(), f.hh, occ.ID, f.kid.ID, f.adult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusApproved {
		t.Errorf("status = %s, want approved", c.Status)
	}
	if b := f.balance(f.adult); b.Points != 3 {
		t.Errorf("adult balance = %+v, want 3 points", b)
	}
}

func TestDoForRefused(t *testing.T) {
	cases := []struct {
		name  string
		input func(f *fixture) Input
		child func(f *fixture) int64
		by    func(f *fixture) int64
		want  error
	}{
		{
			"a child can't", func(f *fixture) Input { return Input{Mode: ModeAssigned, AssigneeID: &f.kid.ID} },
			func(f *fixture) int64 { return f.kid.ID }, func(f *fixture) int64 { return f.kid2.ID }, ErrNotAdult,
		},
		{
			"someone else's chore", func(f *fixture) Input { return Input{Mode: ModeAssigned, AssigneeID: &f.kid.ID} },
			func(f *fixture) int64 { return f.kid2.ID }, func(f *fixture) int64 { return f.adult.ID }, ErrNotYours,
		},
		{
			"an unclaimed pool chore", func(f *fixture) Input { return Input{Mode: ModePool} },
			func(f *fixture) int64 { return f.kid.ID }, func(f *fixture) int64 { return f.adult.ID }, ErrNotYours,
		},
		{
			"not for an adult", func(f *fixture) Input { return Input{Mode: ModeAssigned, AssigneeID: &f.adult.ID} },
			func(f *fixture) int64 { return f.adult.ID }, func(f *fixture) int64 { return f.adult.ID }, ErrNotChild,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.adultChore("Make bed", tc.input(f))
			occ := f.onlyOccurrence()
			if _, err := f.svc.DoFor(t.Context(), f.hh, occ.ID, tc.child(f), tc.by(f)); !errors.Is(err, tc.want) {
				t.Errorf("DoFor = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDoForShared(t *testing.T) {
	f := newFixture(t)
	f.adultChore("Tidy up", Input{Mode: ModeShared, PeopleIDs: []int64{f.kid.ID, f.kid2.ID}})
	occ := f.onlyOccurrence()
	if _, err := f.svc.DoFor(t.Context(), f.hh, occ.ID, f.kid2.ID, f.adult.ID); err != nil {
		t.Fatalf("DoFor: %v", err)
	}
	b := f.boardAt("2026-11-02")
	if isla := b.column("Isla"); len(isla.Cards) != 1 || isla.Cards[0].DoneBy != "Mum" || isla.DoneToday != 1 {
		t.Errorf("Isla's column = %+v, want Tidy up done by Mum", isla)
	}
	if mum := b.column("Mum"); len(mum.Cards) != 0 {
		t.Errorf("Mum's column has %d cards, want none", len(mum.Cards))
	}
	if b := f.balance(f.adult); b.Points != 3 {
		t.Errorf("adult balance = %+v, want 3 points", b)
	}
}

func TestDoForEach(t *testing.T) {
	f := newFixture(t)
	f.adultChore("Brush teeth", Input{Mode: ModeEach, PeopleIDs: []int64{f.kid.ID, f.kid2.ID}})
	if _, err := f.svc.Materialise(t.Context(), f.hh, f.svc.now(), 1); err != nil {
		t.Fatal(err)
	}
	var alfies int64
	for _, r := range f.board("2026-11-02", "2026-11-02") {
		if r.AssigneeID != nil && *r.AssigneeID == f.kid.ID {
			alfies = r.ID
		}
	}
	if alfies == 0 {
		t.Fatal("no occurrence for Alfie")
	}
	if _, err := f.svc.DoFor(t.Context(), f.hh, alfies, f.kid.ID, f.adult.ID); err != nil {
		t.Fatalf("DoFor: %v", err)
	}
	b := f.boardAt("2026-11-02")
	if a := b.column("Alfie"); a.DoneToday != 1 {
		t.Errorf("Alfie done %d, want 1", a.DoneToday)
	}
	if i := b.column("Isla"); i.DoneToday != 0 {
		t.Errorf("Isla done %d, want 0", i.DoneToday)
	}
}
