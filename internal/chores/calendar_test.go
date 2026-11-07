package chores

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func (f *fixture) calendar(day, from, to string) map[string][]CalChore {
	f.t.Helper()
	d, err := time.ParseInLocation(DateLayout, day, london)
	if err != nil {
		f.t.Fatal(err)
	}
	cal, err := f.svc.Calendar(f.t.Context(), f.hh, d, from, to)
	if err != nil {
		f.t.Fatal(err)
	}
	return cal
}

func calTitles(cs []CalChore) string {
	var out []string
	for _, c := range cs {
		s := c.Title + ":" + string(c.Status)
		if c.OccurrenceID == 0 {
			s += "*"
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func TestCalendar(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	daily := "FREQ=DAILY"
	weekly := "FREQ=WEEKLY;BYDAY=MO"
	teeth := f.chore("Teeth", choreOpts{assignee: &f.kid.ID, rrule: &daily, startsOn: "2026-10-26"})
	f.chore("Bins", choreOpts{rrule: &weekly, startsOn: "2026-10-26"})
	f.chore("Car", choreOpts{startsOn: "2026-11-20"}) // one-off, past the window
	gone := f.chore("Gone", choreOpts{assignee: &f.kid.ID, rrule: &daily, startsOn: "2026-10-26"})
	later := f.chore("Later", choreOpts{assignee: &f.kid2.ID, rrule: &daily, startsOn: "2026-12-01"})
	if _, err := f.st.Q.UpdateChore(ctx, dbq.UpdateChoreParams{
		HouseholdID: f.hh, ID: later.ID, Title: "Later", Mode: ModeAssigned, AssigneeID: &f.kid2.ID,
		Rrule: &daily, StartsOn: "2026-12-01", DueTimes: "07:30", UpdatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Materialise(ctx, f.hh, time.Date(2026, 10, 26, 0, 0, 0, 0, london), 7+Window); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Q.ArchiveChore(ctx, dbq.ArchiveChoreParams{HouseholdID: f.hh, ID: gone.ID, ArchivedAt: ptr(int64(2)), UpdatedAt: 2}); err != nil {
		t.Fatal(err)
	}

	// Past days: 27 Oct done, 28 Oct skipped, 29 Oct missed.
	rows := f.board("2026-10-26", "2026-11-30")
	occ := map[string]int64{}
	for _, r := range rows {
		occ[r.Title+r.DueOn] = r.ID
	}
	if _, err := f.svc.Complete(ctx, f.hh, occ["Teeth2026-10-27"], f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Q.SetOccurrenceStatus(ctx, dbq.SetOccurrenceStatusParams{Status: "skipped", HouseholdID: f.hh, ID: occ["Teeth2026-10-28"]}); err != nil {
		t.Fatal(err)
	}
	// Someone does today's pool chore without claiming it.
	if _, err := f.svc.Complete(ctx, f.hh, occ["Bins2026-11-02"], f.kid2.ID); err != nil {
		t.Fatal(err)
	}

	cal := f.calendar("2026-11-02", "2026-10-26", "2026-12-07")
	for day, want := range map[string]string{
		"2026-10-27": "Teeth:done",
		"2026-10-28": "",
		"2026-10-29": "Teeth:missed",
		"2026-11-02": "Bins:done, Teeth:today",
		"2026-11-03": "Teeth:upcoming",
		"2026-11-20": "Car:upcoming*, Teeth:upcoming*", // projected past the 14-day window
		"2026-11-30": "Bins:upcoming*, Teeth:upcoming*",
		"2026-12-01": "Later:upcoming*, Teeth:upcoming*", // timed first
	} {
		if got := calTitles(cal[day]); got != want {
			t.Errorf("%s: got %q, want %q", day, got, want)
		}
	}
	done := cal["2026-10-27"][0]
	if done.DoneByID == nil || *done.DoneByID != f.kid.ID || done.DoneAt == 0 {
		t.Errorf("done = %+v, want done by Alfie", done)
	}
	bins := cal["2026-11-02"][0]
	if !bins.Pool || bins.PersonID == nil || *bins.PersonID != f.kid2.ID {
		t.Errorf("done pool chore = %+v, want shown under Isla", bins)
	}
	if b := cal["2026-11-09"]; len(b) != 2 || !b[0].Pool || b[0].PersonID != nil {
		t.Errorf("upcoming pool chore = %+v, want up for grabs", b)
	}

	// Claimed pool chores belong to the claimer.
	if err := f.svc.Claim(ctx, f.hh, occ["Bins2026-11-09"], f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if b := f.calendar("2026-11-02", "2026-11-09", "2026-11-09")["2026-11-09"][0]; b.PersonID == nil || *b.PersonID != f.kid.ID {
		t.Errorf("claimed = %+v, want Alfie's", b)
	}

	// Nothing projected before today, and nothing written.
	before := len(f.board("2026-10-01", "2027-01-31"))
	if got := f.calendar("2026-11-02", "2026-10-01", "2026-10-25"); len(got) != 0 {
		t.Errorf("before the chores started: %+v", got)
	}
	if after := len(f.board("2026-10-01", "2027-01-31")); after != before {
		t.Errorf("Calendar wrote occurrences: %d → %d", before, after)
	}

	// ChoreOn.
	d := time.Date(2026, 11, 2, 0, 0, 0, 0, london)
	c, ch, err := f.svc.ChoreOn(ctx, f.hh, d, teeth.ID, "2026-11-25")
	if err != nil || c.Status != CalUpcoming || ch.ID != teeth.ID {
		t.Errorf("ChoreOn = %+v, %v", c, err)
	}
	if _, _, err := f.svc.ChoreOn(ctx, f.hh, d, later.ID, "2026-11-25"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChoreOn before it starts = %v, want ErrNotFound", err)
	}
	if _, _, err := f.svc.ChoreOn(ctx, f.hh, d, gone.ID, "2026-11-03"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChoreOn archived = %v, want ErrNotFound", err)
	}
}
