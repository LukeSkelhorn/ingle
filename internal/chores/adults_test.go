package chores

import (
	"testing"
)

// adultChore creates a daily chore worth 5 points and 50p to kids and 3
// points to adults through the admin path.
func (f *fixture) adultChore(title string, in Input) {
	f.t.Helper()
	in.Title, in.Repeat, in.StartsOn = title, Recurrence{Kind: RepeatDaily}, "2026-11-02"
	in.Points, in.Pence, in.AdultPoints = 5, 50, 3
	if _, err := f.svc.CreateChore(f.t.Context(), f.hh, f.adult.ID, in, f.svc.now()); err != nil {
		f.t.Fatalf("CreateChore: %v", err)
	}
}

func TestEarns(t *testing.T) {
	if p, m := Earns("adult", 5, 50, 3); p != 3 || m != 0 {
		t.Errorf("adult earns %d, %d; want 3, 0", p, m)
	}
	if p, m := Earns("child", 5, 50, 3); p != 5 || m != 50 {
		t.Errorf("child earns %d, %d; want 5, 50", p, m)
	}
}

// Adults get a chore's adults' points and no money however it's given out;
// children still get its points and money.
func TestAdultsEarnPointsOnly(t *testing.T) {
	cases := []struct {
		name  string
		input func(f *fixture) Input
		claim bool // claim it first (an Up for grabs chore)
	}{
		{"someone specific", func(f *fixture) Input { return Input{Mode: ModeAssigned, AssigneeID: &f.adult.ID} }, false},
		{"up for grabs, done straight away", func(f *fixture) Input { return Input{Mode: ModePool} }, false},
		{"up for grabs, claimed", func(f *fixture) Input { return Input{Mode: ModePool} }, true},
		{"any of these people", func(f *fixture) Input {
			return Input{Mode: ModeShared, PeopleIDs: []int64{f.adult.ID, f.kid.ID}}
		}, false},
		{"each of these people", func(f *fixture) Input { return Input{Mode: ModeEach, PeopleIDs: []int64{f.adult.ID}} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			f.adultChore("Hoover", tc.input(f))
			occ := f.onlyOccurrence()
			if tc.claim {
				if err := f.svc.Claim(ctx, f.hh, occ.ID, f.adult.ID); err != nil {
					t.Fatalf("Claim: %v", err)
				}
			}
			c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.adult.ID)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if c.Points != 3 || c.Pence != 0 {
				t.Errorf("completion = %d pts, %dp; want 3 pts, 0p", c.Points, c.Pence)
			}
			if b := f.balance(f.adult); b.Points != 3 || b.Pence != 0 {
				t.Errorf("adult balance = %+v, want 3 points, no money", b)
			}
		})
	}

	t.Run("a child still gets both", func(t *testing.T) {
		f := newFixture(t)
		f.adultChore("Hoover", Input{Mode: ModePool})
		occ := f.onlyOccurrence()
		if _, err := f.svc.Complete(t.Context(), f.hh, occ.ID, f.kid.ID); err != nil {
			t.Fatal(err)
		}
		if b := f.balance(f.kid); b.Points != 5 || b.Pence != 50 {
			t.Errorf("child balance = %+v, want 5 points, 50p", b)
		}
	})
}

// On the board, a shared chore shows each column what that person would
// earn, and adults' columns show no money.
func TestAdultsOnTheBoard(t *testing.T) {
	f := newFixture(t)
	f.adultChore("Bins", Input{Mode: ModeShared, PeopleIDs: []int64{f.adult.ID, f.kid.ID}})
	f.adultChore("Dishes", Input{Mode: ModeAssigned, AssigneeID: &f.adult.ID})

	b := f.boardAt("2026-11-02")
	mum, alfie := b.column("Mum"), b.column("Alfie")
	if c, ok := cardTitled(mum, "Bins"); !ok || c.Points != 3 || c.Pence != 0 {
		t.Errorf("Mum's Bins = %+v, want 3 pts, no money", c)
	}
	if c, ok := cardTitled(alfie, "Bins"); !ok || c.Points != 5 || c.Pence != 50 {
		t.Errorf("Alfie's Bins = %+v, want 5 pts, 50p", c)
	}
	if c, ok := cardTitled(mum, "Dishes"); !ok || c.Points != 3 || c.Pence != 0 {
		t.Errorf("Mum's Dishes = %+v, want 3 pts, no money", c)
	}
}

// Money an adult had before stays in the ledger but isn't shown as theirs.
func TestAdultsHaveNoMoneyBalance(t *testing.T) {
	f := newFixture(t)
	f.chore("Old", choreOpts{assignee: &f.kid.ID, points: 1, pence: 75})
	occ := f.onlyOccurrence()
	if _, err := f.svc.Complete(t.Context(), f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	// Alfie grows up: his money stays in the ledger.
	if _, err := f.st.SQL().ExecContext(t.Context(), "UPDATE people SET kind = 'adult' WHERE id = ?", f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if b := f.balance(f.kid); b.Pence != 75 {
		t.Fatalf("ledger pence = %d, want 75 kept", b.Pence)
	}
	sum, err := f.svc.PersonSummary(t.Context(), f.hh, f.kid.ID, f.svc.now().AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Points != 1 || sum.Pence != 0 || sum.WeekPence != 0 {
		t.Errorf("summary = %+v, want 1 point and no money", sum)
	}
	if col := f.boardAt("2026-11-02").column("Alfie"); col.Pence != 0 {
		t.Errorf("column pence = %d, want 0", col.Pence)
	}
}
