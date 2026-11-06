package chores

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/valid"
)

// sharedChore creates a daily chore through the admin path, for people.
func (f *fixture) sharedChore(title string, people []int64, mut ...func(*Input)) int64 {
	f.t.Helper()
	in := Input{
		Title: title, Mode: ModeShared, PeopleIDs: people,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02", Points: 4,
	}
	for _, m := range mut {
		m(&in)
	}
	c, err := f.svc.CreateChore(f.t.Context(), f.hh, f.adult.ID, in, f.svc.now())
	if err != nil {
		f.t.Fatalf("CreateChore: %v", err)
	}
	return c.ID
}

func cardTitled(col Column, title string) (Card, bool) {
	for _, c := range col.Cards {
		if c.Title == title {
			return c, true
		}
	}
	return Card{}, false
}

func TestSharedChoreBoard(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.sharedChore("School run", []int64{f.adult.ID, f.kid2.ID})

	b := f.boardAt("2026-11-02")
	if len(b.Pool) != 0 {
		t.Errorf("pool = %v, want nothing", titles(b.Pool))
	}
	if _, ok := cardTitled(b.column("Alfie"), "School run"); ok {
		t.Error("shown for Alfie, who isn't chosen")
	}
	mum, ok := cardTitled(b.column("Mum"), "School run")
	if !ok || !mum.Shared || mum.Pool || mum.For != f.adult.ID || len(mum.With) != 1 || mum.With[0].ID != f.kid2.ID {
		t.Fatalf("Mum's card = %+v, ok %v", mum, ok)
	}
	isla, ok := cardTitled(b.column("Isla"), "School run")
	if !ok || isla.For != f.kid2.ID || len(isla.With) != 1 || isla.With[0].ID != f.adult.ID {
		t.Fatalf("Isla's card = %+v, ok %v", isla, ok)
	}
	if b.column("Mum").TotalToday != 1 || b.column("Isla").TotalToday != 1 {
		t.Errorf("totals Mum %d, Isla %d; want 1 each", b.column("Mum").TotalToday, b.column("Isla").TotalToday)
	}

	// Isla does it: gone from Mum's column, ticked in Isla's, points to Isla.
	comp, err := f.svc.Complete(ctx, f.hh, isla.OccurrenceID, f.kid2.ID)
	if err != nil {
		t.Fatal(err)
	}
	b = f.boardAt("2026-11-02")
	if _, ok := cardTitled(b.column("Mum"), "School run"); ok || b.column("Mum").TotalToday != 0 {
		t.Errorf("still in Mum's column after Isla did it (total %d)", b.column("Mum").TotalToday)
	}
	if c, ok := cardTitled(b.column("Isla"), "School run"); !ok || !c.Done {
		t.Errorf("Isla's card = %+v, want ticked", c)
	}
	if col := b.column("Isla"); col.DoneToday != 1 || col.TotalToday != 1 {
		t.Errorf("Isla progress %d/%d, want 1/1", col.DoneToday, col.TotalToday)
	}
	if bal := f.balance(f.kid2); bal.Points != 4 {
		t.Errorf("Isla points = %d, want 4", bal.Points)
	}
	if bal := f.balance(f.adult); bal.Points != 0 {
		t.Errorf("Mum points = %d, want 0", bal.Points)
	}

	// Undo puts it back for both of them.
	if err := f.svc.Undo(ctx, f.hh, comp.ID); err != nil {
		t.Fatal(err)
	}
	b = f.boardAt("2026-11-02")
	for _, name := range []string{"Mum", "Isla"} {
		if c, ok := cardTitled(b.column(name), "School run"); !ok || c.Done {
			t.Errorf("%s after undo: %+v, ok %v", name, c, ok)
		}
	}
	if bal := f.balance(f.kid2); bal.Points != 0 {
		t.Errorf("Isla points after undo = %d", bal.Points)
	}
}

func TestSharedChoreOnlyItsPeople(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.sharedChore("Make tea", []int64{f.adult.ID, f.kid2.ID})
	occ := f.onlyOccurrence()
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("Alfie completing = %v, want ErrNotYours", err)
	}
	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrNotClaimable) {
		t.Errorf("Claim = %v, want ErrNotClaimable", err)
	}
	if !f.svc.IsShared(ctx, f.hh, occ.ID) {
		t.Error("IsShared = false")
	}
}

func TestSharedChoreApprovalAndFlex(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.sharedChore("Stairs", []int64{f.kid.ID, f.kid2.ID}, func(in *Input) {
		in.Repeat = Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Tuesday, time.Thursday}}
		in.DayMode = DayAny
		in.RequiresApproval = true
	})
	tue := time.Date(2026, 11, 3, 10, 0, 0, 0, london)
	f.svc.now = func() time.Time { return tue }
	b := f.boardAt("2026-11-03")
	a, ok := cardTitled(b.column("Alfie"), "Stairs")
	if !ok || a.Flex != "Today or Thu" {
		t.Fatalf("Alfie Tue = %+v", a)
	}
	if _, err := f.svc.Complete(ctx, f.hh, a.OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	b = f.boardAt("2026-11-03")
	if c, ok := cardTitled(b.column("Alfie"), "Stairs"); !ok || !c.Pending {
		t.Errorf("Alfie after = %+v, want pending tick", c)
	}
	if _, ok := cardTitled(b.column("Isla"), "Stairs"); ok {
		t.Error("still in Isla's column while pending")
	}
	if _, ok := cardTitled(f.boardAt("2026-11-05").column("Isla"), "Stairs"); ok {
		t.Error("back for Isla on Thursday after Alfie did it")
	}
}

func TestSharedChoreCalendar(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.sharedChore("School run", []int64{f.adult.ID, f.kid2.ID})
	mon := time.Date(2026, 11, 2, 0, 0, 0, 0, london)
	cal, err := f.svc.Calendar(ctx, f.hh, mon, "2026-11-02", "2026-11-30")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-11-02", "2026-11-28"} { // materialised and projected
		cs := cal[d]
		if len(cs) != 1 || cs[0].PersonID != nil || cs[0].Pool || !slices.Equal(cs[0].People, []int64{f.adult.ID, f.kid2.ID}) {
			t.Errorf("%s = %+v", d, cs)
		}
	}
	occ := f.onlyOccurrence()
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	cal, _ = f.svc.Calendar(ctx, f.hh, mon, "2026-11-02", "2026-11-02")
	if c := cal["2026-11-02"]; len(c) != 1 || c[0].Status != CalDone || c[0].PersonID == nil || *c[0].PersonID != f.adult.ID || c[0].People != nil {
		t.Errorf("done = %+v", c)
	}
}

func TestSharedChoreAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.svc.now()
	id := f.sharedChore("School run", []int64{f.adult.ID, f.kid2.ID, f.adult.ID})
	if got, _ := f.svc.ChorePeople(ctx, f.hh, id); !slices.Equal(got, []int64{f.adult.ID, f.kid2.ID}) {
		t.Errorf("people = %v", got)
	}

	// One person is just an assigned chore.
	one := f.sharedChore("Bath", []int64{f.kid.ID, 9999})
	c, _ := f.svc.Chore(ctx, f.hh, one)
	if c.Mode != ModeAssigned || c.AssigneeID == nil || *c.AssigneeID != f.kid.ID {
		t.Errorf("one person = %+v, want assigned to Alfie", c)
	}
	if got, _ := f.svc.ChorePeople(ctx, f.hh, one); len(got) != 0 {
		t.Errorf("assigned chore has people %v", got)
	}

	// Nobody is an error.
	var ve valid.Errors
	_, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, Input{
		Title: "Nobody", Mode: ModeShared, Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
	}, day)
	if !errors.As(err, &ve) || ve["people_ids"] == "" {
		t.Errorf("no people err = %v", err)
	}

	// Switching to the pool clears them.
	if _, err := f.svc.UpdateChore(ctx, f.hh, f.adult.ID, id, Input{
		Title: "School run", Mode: ModePool, PeopleIDs: []int64{f.adult.ID, f.kid2.ID},
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
	}, day); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ChorePeople(ctx, f.hh, id); len(got) != 0 {
		t.Errorf("pool chore has people %v", got)
	}
}
