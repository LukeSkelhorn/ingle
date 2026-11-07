package chores

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func (f *fixture) teeth(times []string, showBefore *int64) dbq.Chore {
	f.t.Helper()
	c, err := f.svc.CreateChore(f.t.Context(), f.hh, f.adult.ID, Input{
		Title: "Brush teeth", Mode: ModeAssigned, AssigneeID: &f.kid.ID,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
		DueTimes: times, ShowBefore: showBefore, Points: 1,
	}, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// cardTimes lists a column's undone cards' times ("" for none).
func cardTimes(c Column) []string {
	var out []string
	for _, k := range c.Cards {
		if k.DueTime == nil {
			out = append(out, "")
		} else {
			out = append(out, *k.DueTime)
		}
	}
	return out
}

func TestChoreTimesValidation(t *testing.T) {
	f := newFixture(t)
	c := f.teeth([]string{"19:30", " 07:30", "07:30", ""}, nil)
	if c.DueTimes != "07:30,19:30" || !slices.Equal(Times(c), []string{"07:30", "19:30"}) {
		t.Errorf("due_times = %q", c.DueTimes)
	}
	bad := func(in Input, field string) {
		t.Helper()
		in.Title, in.Mode, in.AssigneeID, in.StartsOn = "X", ModeAssigned, &f.kid.ID, "2026-11-02"
		if in.Repeat.Kind == "" {
			in.Repeat = Recurrence{Kind: RepeatDaily}
		}
		_, err := f.svc.CreateChore(t.Context(), f.hh, f.adult.ID, in, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
		var ve valid.Errors
		if !errors.As(err, &ve) || ve[field] == "" {
			t.Errorf("%+v: err = %v, want a %s error", in, err, field)
		}
	}
	bad(Input{DueTimes: []string{"07:00", "08:00", "09:00", "10:00", "11:00", "12:00", "13:00"}}, "due_time")
	bad(Input{DueTimes: []string{"7pm"}}, "due_time")
	bad(Input{
		DueTimes: []string{"07:30", "19:30"}, DayMode: DayAny,
		Repeat: Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Tuesday, time.Thursday}},
	}, "due_time")
	fortyFive := int64(45)
	bad(Input{DueTimes: []string{"07:30"}, ShowBefore: &fortyFive}, "show_before")
}

func TestChoreTimesMaterialise(t *testing.T) {
	f := newFixture(t)
	f.teeth([]string{"07:30", "19:30"}, nil)
	f.chore("Hoover", choreOpts{assignee: &f.kid.ID})
	for range 2 { // running again adds nothing, timed or not
		if _, err := f.svc.Materialise(t.Context(), f.hh, f.svc.now(), 1); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, r := range f.board("2026-11-02", "2026-11-02") {
		got = append(got, r.Title+"@"+r.DueTime)
	}
	if want := []string{"Brush teeth@07:30", "Brush teeth@19:30", "Hoover@"}; !slices.Equal(got, want) {
		t.Errorf("occurrences = %q, want %q", got, want)
	}
}

func TestChoreTimesOnTheBoard(t *testing.T) {
	f := newFixture(t)
	hour := DefaultShowBefore
	teeth := f.teeth([]string{"07:30", "19:30"}, &hour)
	f.chore("Hoover", choreOpts{assignee: &f.kid.ID})

	// 06:00: neither time is near, so only the untimed chore shows, and the
	// board wakes at 06:30 for the morning one.
	f.at("2026-11-02", "06:00")
	b := f.boardAt("2026-11-02")
	if got := cardTimes(b.column("Alfie")); !slices.Equal(got, []string{""}) {
		t.Errorf("06:00 cards = %q", got)
	}
	if c := b.column("Alfie"); c.TotalToday != 1 {
		t.Errorf("06:00 total = %d, want 1", c.TotalToday)
	}
	if want := time.Date(2026, 11, 2, 6, 30, 0, 0, london); !b.NextChange.Equal(want) {
		t.Errorf("NextChange = %v, want %v", b.NextChange, want)
	}

	// 06:30: the morning one is on; the evening one wakes the board at 18:30.
	f.at("2026-11-02", "06:30")
	b = f.boardAt("2026-11-02")
	if got := cardTimes(b.column("Alfie")); !slices.Equal(got, []string{"07:30", ""}) {
		t.Errorf("06:30 cards = %q", got)
	}
	if want := time.Date(2026, 11, 2, 18, 30, 0, 0, london); !b.NextChange.Equal(want) {
		t.Errorf("NextChange = %v, want %v", b.NextChange, want)
	}

	// Done in the morning: the evening one is still to come, separately.
	morning := b.column("Alfie").Cards[0].OccurrenceID
	if _, err := f.svc.Complete(t.Context(), f.hh, morning, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	f.at("2026-11-02", "18:29")
	b = f.boardAt("2026-11-02")
	if c := b.column("Alfie"); len(c.Cards) != 2 || !c.Cards[1].Done {
		t.Errorf("18:29 cards = %+v", c.Cards)
	}
	f.at("2026-11-02", "18:30")
	b = f.boardAt("2026-11-02")
	if got := cardTimes(b.column("Alfie")); !slices.Equal(got, []string{"19:30", "", "07:30"}) {
		t.Errorf("18:30 cards = %q (undone first, done last)", got)
	}

	// Tomorrow, yesterday's undone evening one is replaced by today's
	// (which wait for their time again), as a daily chore always is.
	next := func(hhmm string) Board {
		t.Helper()
		f.at("2026-11-03", hhmm)
		return f.boardAt("2026-11-03")
	}
	b = next("06:00")
	var overdue int
	for _, c := range b.column("Alfie").Cards {
		if c.Overdue && c.Title == "Brush teeth" {
			overdue++
		}
	}
	if overdue != 0 {
		// Yesterday's are superseded by today's occurrences of the same chore.
		t.Errorf("overdue = %d, want 0 (superseded)", overdue)
	}

	// All day: both times show from the start of the day.
	if _, err := f.svc.UpdateChore(t.Context(), f.hh, f.adult.ID, teeth.ID, Input{
		Title: "Brush teeth", Mode: ModeAssigned, AssigneeID: &f.kid.ID, Repeat: Recurrence{Kind: RepeatDaily},
		StartsOn: "2026-11-02", DueTimes: []string{"07:30", "19:30"}, Points: 1,
	}, time.Date(2026, 11, 3, 0, 0, 0, 0, london)); err != nil {
		t.Fatal(err)
	}
	b = next("06:00")
	if got := cardTimes(b.column("Alfie")); !slices.Equal(got, []string{"", "07:30", "19:30"}) {
		t.Errorf("all day cards = %q (yesterday's one-off Hoover overdue first)", got)
	}
}

func TestChoreTimesOnTheDay(t *testing.T) {
	f := newFixture(t)
	c := f.teeth([]string{"07:30", "19:30"}, nil)
	if _, err := f.st.Q.SetChoreOnTheDay(t.Context(), dbq.SetChoreOnTheDayParams{OnTheDay: 1, HouseholdID: f.hh, ID: c.ID}); err != nil {
		t.Fatal(err)
	}
	f.at("2026-11-02", "08:00")
	b := f.boardAt("2026-11-02")
	cards := b.column("Alfie").Cards
	if len(cards) != 1 || *cards[0].DueTime != "19:30" || cards[0].Until != "19:30" {
		t.Fatalf("08:00 cards = %+v, want only 19:30", cards)
	}
	missed, err := f.svc.Missed(t.Context(), f.hh, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 1 || *missed[0].DueTime != "07:30" {
		t.Errorf("missed = %+v, want the 07:30", missed)
	}
	if _, err := f.svc.Complete(t.Context(), f.hh, cards[0].OccurrenceID, f.kid.ID); err != nil {
		t.Errorf("19:30 at 08:00: %v", err)
	}
}

func TestChoreTimesCalendar(t *testing.T) {
	f := newFixture(t)
	hour := DefaultShowBefore
	f.teeth([]string{"07:30", "19:30"}, &hour)
	// A day past the materialised window is projected from the rule.
	cal, err := f.svc.Calendar(t.Context(), f.hh, time.Date(2026, 11, 2, 0, 0, 0, 0, london), "2026-12-20", "2026-12-20")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cal["2026-12-20"] {
		got = append(got, *c.DueTime)
	}
	if !slices.Equal(got, []string{"07:30", "19:30"}) {
		t.Errorf("calendar times = %q", got)
	}
}
