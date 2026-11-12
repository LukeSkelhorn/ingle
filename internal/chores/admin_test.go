package chores

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/valid"
)

func TestCreateChoreValidation(t *testing.T) {
	f := newFixture(t)
	day := f.svc.now()
	ghost := int64(9999)
	bad := "25:00"
	_, err := f.svc.CreateChore(t.Context(), f.hh, f.adult.ID, Input{
		Title: "", Mode: ModeAssigned, AssigneeID: &ghost,
		Repeat: Recurrence{Kind: RepeatWeekly}, StartsOn: "02/11/2026", DueTimes: []string{bad}, Points: -1, Pence: -1,
	}, day)
	var ve valid.Errors
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want valid.Errors", err)
	}
	for _, field := range []string{"title", "assignee_id", "days", "starts_on", "due_time", "points", "pence"} {
		if ve[field] == "" {
			t.Errorf("no error for %s (got %v)", field, ve)
		}
	}
}

func TestUpdateChoreReplacesOpenOccurrences(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.svc.now() // Mon 2 Nov 2026
	kid := f.kid.ID
	c, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, Input{
		Title: "Bins", Mode: ModePool,
		Repeat:   Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Monday, time.Thursday}},
		StartsOn: "2026-11-02", Points: 5,
	}, day)
	if err != nil {
		t.Fatal(err)
	}
	rows := f.board("2026-11-02", "2026-11-15")
	if got := dueDates(rows); !slices.Equal(got, []string{"2026-11-02", "2026-11-05", "2026-11-09", "2026-11-12"}) {
		t.Fatalf("initial dates %v", got)
	}
	// Alfie claims Thursday 5th; Isla completes Monday 9th.
	if err := f.svc.Claim(ctx, f.hh, rows[1].ID, kid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Complete(ctx, f.hh, rows[2].ID, f.kid2.ID); err != nil {
		t.Fatal(err)
	}

	// Move to Tuesdays only.
	if _, err := f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, Input{
		Title: "Bins", Mode: ModePool,
		Repeat:   Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Tuesday}},
		StartsOn: "2026-11-02", Points: 5,
	}, day); err != nil {
		t.Fatal(err)
	}
	got := dueDates(f.board("2026-11-02", "2026-11-15"))
	// Claimed Thu 5 and done Mon 9 are kept; open Mon 2 and Thu 12 go;
	// Tuesdays 3 and 10 are added.
	if want := []string{"2026-11-03", "2026-11-05", "2026-11-09", "2026-11-10"}; !slices.Equal(got, want) {
		t.Errorf("after edit %v, want %v", got, want)
	}

	// Archive drops the open ones.
	if err := f.svc.ArchiveChore(ctx, f.hh, f.adult.ID, c.ID, day); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := f.st.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM chore_occurrences WHERE chore_id = ? AND status = 'open'", c.ID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d open occurrences after archive", open)
	}

	acts, err := f.st.Q.ListRecentActivity(ctx, dbqActivityParams(f.hh))
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, a := range acts {
		if a.SubjectType == "chore" {
			verbs = append(verbs, a.Verb)
		}
	}
	if !slices.Equal(verbs, []string{"chore.archived", "chore.updated", "chore.created"}) {
		t.Errorf("chore activity %v", verbs)
	}
}

func TestMovingOverdueOneOff(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.svc.now()
	c, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, Input{
		Title: "Tidy room", Mode: ModePool, Repeat: Recurrence{Kind: RepeatOnce}, StartsOn: "2026-10-30",
	}, day)
	if err != nil {
		t.Fatal(err)
	}
	if got := dueDates(f.board("2026-10-01", "2026-11-30")); !slices.Equal(got, []string{"2026-10-30"}) {
		t.Fatalf("one-off %v", got)
	}
	if _, err := f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, Input{
		Title: "Tidy room", Mode: ModePool, Repeat: Recurrence{Kind: RepeatOnce}, StartsOn: "2026-11-04",
	}, day); err != nil {
		t.Fatal(err)
	}
	if got := dueDates(f.board("2026-10-01", "2026-11-30")); !slices.Equal(got, []string{"2026-11-04"}) {
		t.Errorf("moved one-off %v, want only 2026-11-04", got)
	}
}

func TestChoreDayMode(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.svc.now() // Mon 2 Nov 2026
	tueThu := Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Tuesday, time.Thursday}}
	in := Input{Title: "Stairs", Mode: ModePool, Repeat: tueThu, StartsOn: "2026-11-02"}

	c, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, in, day)
	if err != nil {
		t.Fatal(err)
	}
	if c.DayMode != DayEach {
		t.Errorf("default day mode = %q, want each", c.DayMode)
	}
	if got := windows(f.board("2026-11-02", "2026-11-15")); !slices.Equal(got, []string{"-..2026-11-03", "-..2026-11-05", "-..2026-11-10", "-..2026-11-12"}) {
		t.Fatalf("each rows = %v", got)
	}

	// Switching to any re-materialises one row a week.
	in.DayMode = DayAny
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day); err != nil {
		t.Fatal(err)
	}
	if c.DayMode != DayAny {
		t.Errorf("day mode = %q, want any", c.DayMode)
	}
	if got := windows(f.board("2026-11-02", "2026-11-15")); !slices.Equal(got, []string{"2026-11-03..2026-11-05", "2026-11-10..2026-11-12"}) {
		t.Fatalf("any rows = %v", got)
	}

	// Edited on Wednesday: this week starts fresh from Wednesday.
	wed := time.Date(2026, 11, 4, 0, 0, 0, 0, london)
	in.Points = 3
	if _, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, wed); err != nil {
		t.Fatal(err)
	}
	if got := windows(f.board("2026-11-04", "2026-11-08")); !slices.Equal(got, []string{"2026-11-05..2026-11-05"}) {
		t.Errorf("after mid-week edit = %v", got)
	}

	// Daily and monthly have no days to choose between: stored as each.
	in.Repeat = Recurrence{Kind: RepeatDaily}
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day); err != nil {
		t.Fatal(err)
	}
	if c.DayMode != DayEach {
		t.Errorf("daily day mode = %q, want each", c.DayMode)
	}

	in.DayMode = "sometimes"
	var ve valid.Errors
	if _, err := f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day); !errors.As(err, &ve) || ve["day_mode"] == "" {
		t.Errorf("bad day mode err = %v", err)
	}
}
