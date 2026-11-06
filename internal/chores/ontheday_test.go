package chores

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// at sets the fixture's clock to a London wall-clock time.
func (f *fixture) at(day, clock string) {
	f.t.Helper()
	t, err := time.ParseInLocation(DateLayout+" 15:04", day+" "+clock, london)
	if err != nil {
		f.t.Fatal(err)
	}
	f.svc.now = func() time.Time { return t }
}

func TestCutoff(t *testing.T) {
	tests := []struct {
		dueTime  *string
		dayStart time.Duration
		want     string
	}{
		{ptr("07:00"), 0, "2026-11-02 07:00"},
		{nil, 0, "2026-11-03 00:00"},
		{nil, 4 * time.Hour, "2026-11-03 04:00"},
		{ptr("02:00"), 4 * time.Hour, "2026-11-03 02:00"}, // late on the board day
		{ptr("19:30"), 4 * time.Hour, "2026-11-02 19:30"},
	}
	for _, tt := range tests {
		got, ok := cutoff("2026-11-02", tt.dueTime, london, tt.dayStart)
		if !ok || got.Format("2006-01-02 15:04") != tt.want {
			t.Errorf("cutoff(%v, %v) = %v, %v; want %s", tt.dueTime, tt.dayStart, got, ok, tt.want)
		}
	}
	// Clocks go back on Sun 25 Oct 2026: still 07:00 local.
	if got, _ := cutoff("2026-10-25", ptr("07:00"), london, 0); got.Hour() != 7 {
		t.Errorf("cutoff across DST = %v", got)
	}
}

func TestOnTheDayTimed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins out", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), dueTime: ptr("07:00"), onTheDay: true, points: 5})
	f.chore("Tidy room", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), dueTime: ptr("07:00"), points: 1})

	f.at("2026-11-02", "06:30")
	b := f.boardAt("2026-11-02")
	bins, ok := cardTitled(b.column("Alfie"), "Bins out")
	if !ok || !bins.OnTheDay || bins.Until != "07:00" {
		t.Fatalf("bins at 06:30 = %+v, ok %v", bins, ok)
	}
	if tidy, _ := cardTitled(b.column("Alfie"), "Tidy room"); tidy.OnTheDay || tidy.Until != "" {
		t.Errorf("tidy = %+v, want an ordinary card", tidy)
	}
	if want := time.Date(2026, 11, 2, 7, 0, 0, 0, london); !b.NextChange.Equal(want) {
		t.Errorf("NextChange = %v, want %v", b.NextChange, want)
	}

	// At 07:00 it's gone and can't be ticked; the ordinary one stays.
	f.at("2026-11-02", "07:00")
	b = f.boardAt("2026-11-02")
	if _, ok := cardTitled(b.column("Alfie"), "Bins out"); ok {
		t.Error("bins still on the board at 07:00")
	}
	if _, ok := cardTitled(b.column("Alfie"), "Tidy room"); !ok {
		t.Error("tidy room gone at 07:00")
	}
	if !b.NextChange.IsZero() {
		t.Errorf("NextChange = %v, want none", b.NextChange)
	}
	if _, err := f.svc.Complete(ctx, f.hh, bins.OccurrenceID, f.kid.ID); !errors.Is(err, ErrTooLate) {
		t.Fatalf("late Complete = %v, want ErrTooLate", err)
	}

	// Next day: yesterday's isn't Overdue.
	f.at("2026-11-03", "06:00")
	b = f.boardAt("2026-11-03")
	var bins2 int
	for _, c := range b.column("Alfie").Cards {
		switch {
		case c.Title == "Bins out" && c.Overdue:
			t.Error("missed bins shown as Overdue")
		case c.Title == "Bins out":
			bins2++
		}
	}
	if bins2 != 1 {
		t.Errorf("bins cards on Tue = %d, want today's only", bins2)
	}
	if f.balance(f.kid).Points != 0 {
		t.Error("points given for a missed chore")
	}
}

func TestOnTheDayWeeklyNotOverdue(t *testing.T) {
	f := newFixture(t)
	// Weekly on Mondays: an ordinary chore would be Overdue on Tuesday.
	f.chore("Bins out", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), onTheDay: true})
	f.chore("Recycling", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO")})

	f.at("2026-11-02", "23:59")
	b := f.boardAt("2026-11-02")
	if c, ok := cardTitled(b.column("Alfie"), "Bins out"); !ok || c.Until != "" || !c.OnTheDay {
		t.Fatalf("bins late Monday = %+v, ok %v", c, ok)
	}
	f.at("2026-11-03", "00:00")
	b = f.boardAt("2026-11-03")
	if _, ok := cardTitled(b.column("Alfie"), "Bins out"); ok {
		t.Error("bins still up on Tuesday")
	}
	if c, ok := cardTitled(b.column("Alfie"), "Recycling"); !ok || !c.Overdue {
		t.Errorf("recycling on Tuesday = %+v, ok %v; want Overdue", c, ok)
	}
}

func TestOnTheDayDayStart(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.svc.SetDayStart(func(_ context.Context, _ int64) time.Duration { return 4 * time.Hour })
	f.chore("Milk bottles", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), onTheDay: true, points: 2})

	// 03:00 Tuesday is still Monday's board day.
	f.at("2026-11-03", "03:00")
	b := f.boardAt("2026-11-02")
	milk, ok := cardTitled(b.column("Alfie"), "Milk bottles")
	if !ok {
		t.Fatal("milk gone before the day rolled over")
	}
	if want := time.Date(2026, 11, 3, 4, 0, 0, 0, london); !b.NextChange.Equal(want) {
		t.Errorf("NextChange = %v, want %v", b.NextChange, want)
	}
	f.at("2026-11-03", "04:00")
	if _, err := f.svc.Complete(ctx, f.hh, milk.OccurrenceID, f.kid.ID); !errors.Is(err, ErrTooLate) {
		t.Fatalf("Complete at 04:00 = %v, want ErrTooLate", err)
	}
	f.at("2026-11-03", "03:59")
	if _, err := f.svc.Complete(ctx, f.hh, milk.OccurrenceID, f.kid.ID); err != nil {
		t.Fatalf("Complete at 03:59 = %v", err)
	}
}

func TestOnTheDayDoneStaysTicked(t *testing.T) {
	f := newFixture(t)
	f.chore("Bins out", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), dueTime: ptr("07:00"), onTheDay: true, points: 5})
	f.at("2026-11-02", "06:30")
	b := f.boardAt("2026-11-02")
	bins, _ := cardTitled(b.column("Alfie"), "Bins out")
	if _, err := f.svc.Complete(t.Context(), f.hh, bins.OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	f.at("2026-11-02", "08:00")
	b = f.boardAt("2026-11-02")
	if c, ok := cardTitled(b.column("Alfie"), "Bins out"); !ok || !c.Done || c.OnTheDay || c.Until != "" {
		t.Errorf("done bins at 08:00 = %+v, ok %v; want ticked", c, ok)
	}
	if f.balance(f.kid).Points != 5 {
		t.Error("points not kept")
	}
}

func TestOnTheDayPoolClaimed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins out", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), dueTime: ptr("08:00"), onTheDay: true})
	f.chore("Milk out", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), dueTime: ptr("08:00"), onTheDay: true})

	f.at("2026-11-02", "07:00")
	b := f.boardAt("2026-11-02")
	if len(b.Pool) != 2 || b.Pool[0].Until != "08:00" {
		t.Fatalf("pool = %+v", b.Pool)
	}
	if err := f.svc.Claim(ctx, f.hh, b.Pool[0].OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	other := b.Pool[1].OccurrenceID

	f.at("2026-11-02", "08:00")
	b = f.boardAt("2026-11-02")
	if len(b.Pool) != 0 || len(b.column("Alfie").Cards) != 0 {
		t.Errorf("after 08:00 pool %v, Alfie %v; want both empty", titles(b.Pool), titles(b.column("Alfie").Cards))
	}
	if err := f.svc.Claim(ctx, f.hh, other, f.kid2.ID); !errors.Is(err, ErrTooLate) {
		t.Errorf("late Claim = %v, want ErrTooLate", err)
	}
}

func TestOnTheDayFlexible(t *testing.T) {
	f := newFixture(t)
	f.chore("Plants", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=TU,TH"), anyDay: true, dueTime: ptr("18:00"), onTheDay: true})

	f.at("2026-11-03", "19:00") // Tuesday, after the time: Thursday is still to come
	b := f.boardAt("2026-11-03")
	if c, ok := cardTitled(b.column("Alfie"), "Plants"); !ok || c.Flex == "" {
		t.Fatalf("Tuesday evening = %+v, ok %v; want flexible card", c, ok)
	}
	f.at("2026-11-05", "17:00")
	b = f.boardAt("2026-11-05")
	if c, ok := cardTitled(b.column("Alfie"), "Plants"); !ok || !c.LastChance || c.Until != "18:00" {
		t.Fatalf("Thursday = %+v, ok %v; want last chance until 18:00", c, ok)
	}
	f.at("2026-11-05", "18:00")
	if _, ok := cardTitled(f.boardAt("2026-11-05").column("Alfie"), "Plants"); ok {
		t.Error("still up after the last day's time")
	}
	f.at("2026-11-06", "09:00")
	if _, ok := cardTitled(f.boardAt("2026-11-06").column("Alfie"), "Plants"); ok {
		t.Error("carried over to Friday")
	}
}

func TestOnTheDayCalendar(t *testing.T) {
	f := newFixture(t)
	f.chore("Bins out", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), dueTime: ptr("07:00"), onTheDay: true})
	f.materialise("2026-11-02", 3)
	f.at("2026-11-03", "06:00")
	if got := calTitles(f.calendar("2026-11-03", "2026-11-03", "2026-11-03")["2026-11-03"]); got != "Bins out:today" {
		t.Errorf("before = %s", got)
	}
	f.at("2026-11-03", "07:00")
	if got := calTitles(f.calendar("2026-11-03", "2026-11-03", "2026-11-04")["2026-11-03"]); got != "Bins out:missed" {
		t.Errorf("after = %s", got)
	}
}

func TestMissedAndMarkDone(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins out", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), dueTime: ptr("07:00"), onTheDay: true, points: 5, approval: true})
	f.chore("Milk out", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), onTheDay: true, points: 2})
	f.chore("Tidy room", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO")}) // not on the day
	f.chore("Feed fish", choreOpts{assignee: &f.kid2.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), onTheDay: true})
	f.sharedChore("Lunchboxes", []int64{f.kid.ID, f.kid2.ID}, func(in *Input) {
		in.Repeat = Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Monday}}
		in.OnTheDay = true
	})

	// Isla feeds the fish in time.
	f.at("2026-11-02", "06:00")
	b := f.boardAt("2026-11-02")
	fish, _ := cardTitled(b.column("Isla"), "Feed fish")
	if _, err := f.svc.Complete(ctx, f.hh, fish.OccurrenceID, f.kid2.ID); err != nil {
		t.Fatal(err)
	}

	day := func(s string) time.Time { d, _ := time.ParseInLocation(DateLayout, s, london); return d }
	f.at("2026-11-02", "08:00")
	missed, err := f.svc.Missed(ctx, f.hh, day("2026-11-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 1 || missed[0].Title != "Bins out" || missed[0].Person == nil || missed[0].Person.ID != f.kid.ID {
		t.Fatalf("Monday 08:00 missed = %+v; want only bins", missed)
	}

	f.at("2026-11-03", "09:00")
	missed, err = f.svc.Missed(ctx, f.hh, day("2026-11-03"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]MissedDetail{}
	for _, m := range missed {
		got[m.Title] = m
	}
	if len(missed) != 3 {
		t.Fatalf("Tuesday missed = %+v; want bins, milk, lunchboxes", missed)
	}
	if m := got["Milk out"]; m.Person != nil || len(m.Choices) != 3 {
		t.Errorf("milk = %+v; want anyone", m)
	}
	if m := got["Lunchboxes"]; m.Person != nil || len(m.Choices) != 2 {
		t.Errorf("lunchboxes = %+v; want Alfie or Isla", m)
	}

	bins := got["Bins out"].OccurrenceID
	if err := f.svc.MarkDone(ctx, f.hh, bins, f.kid.ID, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("MarkDone by a child = %v", err)
	}
	if err := f.svc.MarkDone(ctx, f.hh, bins, f.kid2.ID, f.adult.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("MarkDone as the wrong person = %v", err)
	}
	if err := f.svc.MarkDone(ctx, f.hh, got["Lunchboxes"].OccurrenceID, f.adult.ID, f.adult.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("MarkDone shared as someone not chosen = %v", err)
	}
	// Needs approval normally; marked by an adult it counts straight away.
	if err := f.svc.MarkDone(ctx, f.hh, bins, f.kid.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if f.balance(f.kid).Points != 5 {
		t.Errorf("Alfie points = %d, want 5", f.balance(f.kid).Points)
	}
	if err := f.svc.MarkDone(ctx, f.hh, bins, f.kid.ID, f.adult.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("MarkDone twice = %v", err)
	}
	comps, err := f.svc.Completions(ctx, f.hh)
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 2 || comps[0].Title != "Bins out" || !comps[0].Counted() || comps[0].ReviewerName != "Mum" {
		t.Errorf("completions = %+v", comps)
	}
	if err := f.svc.MarkDone(ctx, f.hh, got["Milk out"].OccurrenceID, f.kid2.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if f.balance(f.kid2).Points != 2 {
		t.Errorf("Isla points = %d, want 2", f.balance(f.kid2).Points)
	}
	missed, _ = f.svc.Missed(ctx, f.hh, day("2026-11-03"))
	if len(missed) != 1 || missed[0].Title != "Lunchboxes" {
		t.Errorf("after marking, missed = %+v", missed)
	}

	// A week on, Monday 2nd is out of the window.
	f.at("2026-11-09", "06:00")
	missed, _ = f.svc.Missed(ctx, f.hh, day("2026-11-09"))
	for _, m := range missed {
		if m.DueOn == "2026-11-02" {
			t.Errorf("still listed a week later: %+v", m)
		}
	}
}

func TestOnTheDayAdminInput(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	in := Input{
		Title: "Bins out", Mode: ModeAssigned, AssigneeID: &f.kid.ID,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
	}
	c, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, in, f.svc.now())
	if err != nil || c.OnTheDay != 0 {
		t.Fatalf("default = %d, %v; want 0", c.OnTheDay, err)
	}
	in.OnTheDay = true
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, f.svc.now()); err != nil || c.OnTheDay != 1 {
		t.Fatalf("on = %d, %v", c.OnTheDay, err)
	}
	in.OnTheDay = false
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, f.svc.now()); err != nil || c.OnTheDay != 0 {
		t.Fatalf("off = %d, %v", c.OnTheDay, err)
	}
}

func TestDismissMissed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Make bed", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), dueTime: ptr("07:45"), onTheDay: true, points: 3})
	f.chore("Milk out", choreOpts{rrule: ptr("FREQ=DAILY"), onTheDay: true, points: 2})
	f.materialise("2026-11-02", 3)
	day := func(s string) time.Time { d, _ := time.ParseInLocation(DateLayout, s, london); return d }
	ids := func(ms []MissedDetail) []int64 {
		var out []int64
		for _, m := range ms {
			out = append(out, m.OccurrenceID)
		}
		return out
	}

	f.at("2026-11-03", "06:00")
	b := f.boardAt("2026-11-03")
	todayBed, _ := cardTitled(b.column("Alfie"), "Make bed")
	// Not late yet: can't be dismissed.
	if _, err := f.svc.Dismiss(ctx, f.hh, f.adult.ID, []int64{todayBed.OccurrenceID}); !errors.Is(err, ErrNotMissed) {
		t.Errorf("Dismiss before the cutoff = %v", err)
	}

	f.at("2026-11-03", "09:00")
	missed, err := f.svc.Missed(ctx, f.hh, day("2026-11-03"))
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 3 { // Monday's bed and milk, Tuesday's bed
		t.Fatalf("missed = %+v", missed)
	}
	if _, err := f.svc.Dismiss(ctx, f.hh, f.kid.ID, ids(missed[:1])); !errors.Is(err, ErrNotAdult) {
		t.Errorf("Dismiss by a child = %v", err)
	}

	// One.
	one := missed[0].OccurrenceID
	done, err := f.svc.Dismiss(ctx, f.hh, f.adult.ID, []int64{one})
	if err != nil || len(done) != 1 || done[0] != one {
		t.Fatalf("Dismiss = %v, %v", done, err)
	}
	if _, err := f.svc.Dismiss(ctx, f.hh, f.adult.ID, []int64{one}); !errors.Is(err, ErrNotMissed) {
		t.Errorf("Dismiss twice = %v", err)
	}
	after, _ := f.svc.Missed(ctx, f.hh, day("2026-11-03"))
	if len(after) != 2 {
		t.Errorf("after dismissing one, missed = %+v", after)
	}
	if got := calTitles(f.calendar("2026-11-03", "2026-11-03", "2026-11-03")["2026-11-03"]); got != "Make bed:missed, Milk out:today" {
		t.Errorf("calendar after dismiss = %s; want still missed", got)
	}
	if f.balance(f.kid).Points != 0 {
		t.Errorf("points = %d, want 0", f.balance(f.kid).Points)
	}
	if n, err := f.svc.Restore(ctx, f.hh, f.adult.ID, []int64{one}); err != nil || n != 1 {
		t.Fatalf("Restore = %d, %v", n, err)
	}
	if _, err := f.svc.Restore(ctx, f.hh, f.adult.ID, []int64{one}); !errors.Is(err, ErrNotMissed) {
		t.Errorf("Restore twice = %v", err)
	}

	// All, with one marked done in the meantime: that one is skipped.
	all, _ := f.svc.Missed(ctx, f.hh, day("2026-11-03"))
	if len(all) != 3 {
		t.Fatalf("after restore, missed = %+v", all)
	}
	var bed MissedDetail
	for _, m := range all {
		if m.Person != nil {
			bed = m
			break
		}
	}
	if err := f.svc.MarkDone(ctx, f.hh, bed.OccurrenceID, f.kid.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	done, err = f.svc.Dismiss(ctx, f.hh, f.adult.ID, ids(all))
	if err != nil || len(done) != 2 || slices.Contains(done, bed.OccurrenceID) {
		t.Fatalf("Dismiss all = %v, %v", done, err)
	}
	if left, _ := f.svc.Missed(ctx, f.hh, day("2026-11-03")); len(left) != 0 {
		t.Errorf("after dismiss all, missed = %+v", left)
	}
	if n, err := f.svc.Restore(ctx, f.hh, f.adult.ID, done); err != nil || n != 2 {
		t.Errorf("Restore all = %d, %v", n, err)
	}
	if back, _ := f.svc.Missed(ctx, f.hh, day("2026-11-03")); len(back) != 2 {
		t.Errorf("after undo all, missed = %+v", back)
	}
}
