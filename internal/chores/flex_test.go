package chores

import (
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Mon 2 Nov 2026 is the fixture's first day; Tue 3, Thu 5, Sat 7, Sun 8.

func windows(rows []dbq.ListBoardRow) []string {
	var out []string
	for _, r := range rows {
		from := "-"
		if r.AvailableFrom != nil {
			from = *r.AvailableFrom
		}
		out = append(out, from+".."+r.DueOn)
	}
	return out
}

func (f *fixture) materialise(from string, days int) {
	f.t.Helper()
	d, err := time.ParseInLocation(DateLayout, from, london)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.Materialise(f.t.Context(), f.hh, d, days); err != nil {
		f.t.Fatal(err)
	}
}

func TestFlexMaterialise(t *testing.T) {
	f := newFixture(t)
	f.chore("Stairs", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=TU,TH"), anyDay: true})
	// A window ending on a Wednesday still sees that week's Thursday.
	f.materialise("2026-11-02", 9) // Mon 2 – Tue 10
	f.materialise("2026-11-02", 9) // idempotent
	got := windows(f.board("2026-11-01", "2026-11-30"))
	want := []string{"2026-11-03..2026-11-05", "2026-11-10..2026-11-12"}
	if !slices.Equal(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
	// A later run starting mid-week doesn't make a second row for the week.
	f.materialise("2026-11-04", 14)
	got = windows(f.board("2026-11-01", "2026-11-17"))
	want = []string{"2026-11-03..2026-11-05", "2026-11-10..2026-11-12", "2026-11-17..2026-11-19"}
	if !slices.Equal(got, want) {
		t.Fatalf("after rerun windows = %v, want %v", got, want)
	}
}

func TestFlexMaterialiseKinds(t *testing.T) {
	f := newFixture(t)
	f.chore("Car", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=SA,SU"), anyDay: true})
	f.chore("Lawn", choreOpts{rrule: ptr("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR"), anyDay: true})
	f.chore("Tidy", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"), startsOn: "2026-11-04", anyDay: true})
	f.materialise("2026-11-02", 21)
	by := map[string][]string{}
	for _, r := range f.board("2026-11-01", "2026-12-31") {
		by[r.Title] = append(by[r.Title], *r.AvailableFrom+".."+r.DueOn)
	}
	checks := map[string][]string{
		// Sat+Sun is one weekend: weeks are Mon–Sun whatever the week start.
		"Car": {"2026-11-07..2026-11-08", "2026-11-14..2026-11-15", "2026-11-21..2026-11-22"},
		// Every other week only.
		"Lawn": {"2026-11-02..2026-11-06", "2026-11-16..2026-11-20"},
		// Weekdays, starting on a Wednesday: the first week is Wed–Fri.
		"Tidy": {"2026-11-04..2026-11-06", "2026-11-09..2026-11-13", "2026-11-16..2026-11-20"},
	}
	for title, want := range checks {
		if got := by[title]; !slices.Equal(got, want) {
			t.Errorf("%s windows = %v, want %v", title, got, want)
		}
	}
}

func TestFlexBoard(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Stairs", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=TU,TH"), startsOn: "2026-10-01", points: 5, anyDay: true})
	f.chore("Teeth", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), startsOn: "2026-10-01"})

	card := func(b Board) (Card, bool) {
		for _, c := range b.column("Alfie").Cards {
			if c.Title == "Stairs" {
				return c, true
			}
		}
		return Card{}, false
	}

	// Monday isn't one of its days: only last week's missed one is up.
	if c, ok := card(f.boardAt("2026-11-02")); !ok || !c.Overdue || c.DueOn != "2026-10-29" {
		t.Errorf("Mon card = %+v, want last week's, overdue", c)
	}
	// Tuesday: up, labelled, and sorted with today's chores (not by its Thursday due date).
	b := f.boardAt("2026-11-03")
	c, ok := card(b)
	if !ok || c.Flex != "Today or Thu" || c.LastChance || c.Overdue {
		t.Fatalf("Tue card = %+v, want Flex %q", c, "Today or Thu")
	}
	if got := titles(b.column("Alfie").Cards); !slices.Equal(got, []string{"Stairs@2026-11-05", "Teeth@2026-11-03"}) {
		t.Errorf("Tue order = %v", got)
	}
	// Wednesday is between its days: still up, the only day left is Thu.
	if c, ok := card(f.boardAt("2026-11-04")); !ok || c.Flex != "" || c.LastChance {
		t.Errorf("Wed card = %+v, ok %v", c, ok)
	}
	// Thursday: last chance.
	if c, ok := card(f.boardAt("2026-11-05")); !ok || !c.LastChance || c.Flex != "" {
		t.Errorf("Thu card = %+v, want last chance", c)
	}
	// Friday: overdue, until next week's window opens on Tuesday.
	if c, ok := card(f.boardAt("2026-11-06")); !ok || !c.Overdue {
		t.Errorf("Fri card = %+v, want overdue", c)
	}
	if c, ok := card(f.boardAt("2026-11-09")); !ok || !c.Overdue {
		t.Errorf("next Mon card = %+v, want still overdue", c)
	}
	b = f.boardAt("2026-11-10")
	n := 0
	for _, c := range b.column("Alfie").Cards {
		if c.Title == "Stairs" {
			n++
			if c.Overdue || c.DueOn != "2026-11-12" {
				t.Errorf("next Tue card = %+v, want the new week's only", c)
			}
		}
	}
	if n != 1 {
		t.Errorf("next Tue shows Stairs %d times, want once", n)
	}

	// Done on Tuesday: ticked that day, gone for the rest of the week.
	tue := time.Date(2026, 11, 10, 18, 0, 0, 0, london)
	f.svc.now = func() time.Time { return tue }
	c, _ = card(f.boardAt("2026-11-10"))
	comp, err := f.svc.Complete(ctx, f.hh, c.OccurrenceID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := card(f.boardAt("2026-11-10")); !ok || !c.Done || c.Flex != "" {
		t.Errorf("Tue after done = %+v, want ticked", c)
	}
	for _, day := range []string{"2026-11-11", "2026-11-12", "2026-11-13"} {
		if c, ok := card(f.boardAt(day)); ok {
			t.Errorf("%s still shows %+v after it was done", day, c)
		}
	}
	if bal := f.balance(f.kid); bal.Points != 5 {
		t.Errorf("points = %d, want 5 (once for the week)", bal.Points)
	}

	// Undo brings it back for the days left.
	if err := f.svc.Undo(ctx, f.hh, comp.ID); err != nil {
		t.Fatal(err)
	}
	if c, ok := card(f.boardAt("2026-11-12")); !ok || c.Done || !c.LastChance {
		t.Errorf("Thu after undo = %+v, ok %v; want back, last chance", c, ok)
	}
}

func TestFlexPoolClaimCarriesOver(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Car", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=SA,SU"), anyDay: true})
	b := f.boardAt("2026-11-07")
	if len(b.Pool) != 1 || b.Pool[0].Flex != "Today or Sun" {
		t.Fatalf("Sat pool = %+v", b.Pool)
	}
	if err := f.svc.Claim(ctx, f.hh, b.Pool[0].OccurrenceID, f.kid2.ID); err != nil {
		t.Fatal(err)
	}
	b = f.boardAt("2026-11-08")
	cards := b.column("Isla").Cards
	if len(b.Pool) != 0 || len(cards) != 1 || !cards[0].Claimed || !cards[0].LastChance {
		t.Errorf("Sun: pool %v, Isla %+v; want claimed by Isla, last chance", titles(b.Pool), cards)
	}
}

func TestFlexLabels(t *testing.T) {
	cases := []struct {
		days []string
		want string
	}{
		{[]string{"2026-11-03"}, ""},
		{[]string{"2026-11-03", "2026-11-05"}, "Today or Thu"},
		{[]string{"2026-11-03", "2026-11-04", "2026-11-05"}, "Today, Wed or Thu"},
		{[]string{"2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06"}, "Any day until Fri"},
	}
	for _, c := range cases {
		if got := FlexLabel("2026-11-03", c.days); got != c.want {
			t.Errorf("FlexLabel(%v) = %q, want %q", c.days, got, c.want)
		}
	}
	if got := DaysHint([]string{"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06"}); got != "Mon–Fri" {
		t.Errorf("weekdays hint = %q", got)
	}
	if got := DaysHint([]string{"2026-11-07", "2026-11-08"}); got != "Sat or Sun" {
		t.Errorf("weekend hint = %q", got)
	}
}

func TestFlexCalendar(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Stairs", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=TU,TH"), anyDay: true})
	mon := time.Date(2026, 11, 2, 0, 0, 0, 0, london)
	f.materialise("2026-11-02", Window)

	cal := func(boardDay time.Time) map[string][]CalChore {
		t.Helper()
		m, err := f.svc.Calendar(ctx, f.hh, boardDay, "2026-11-02", "2026-12-06")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	days := func(m map[string][]CalChore) []string {
		var out []string
		for d, cs := range m {
			for _, c := range cs {
				out = append(out, d+":"+string(c.Status))
				if c.Or != "Tue or Thu" {
					t.Errorf("%s Or = %q", d, c.Or)
				}
			}
		}
		slices.Sort(out)
		return out
	}

	// Materialised (first two weeks) and projected (after): on each of its days.
	got := days(cal(mon))
	want := []string{
		"2026-11-03:upcoming", "2026-11-05:upcoming", "2026-11-10:upcoming", "2026-11-12:upcoming",
		"2026-11-17:upcoming", "2026-11-19:upcoming", "2026-11-24:upcoming", "2026-11-26:upcoming",
		"2026-12-01:upcoming", "2026-12-03:upcoming",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("calendar = %v\nwant %v", got, want)
	}

	// Done on Tue 10: just that day, ticked; nothing on Thu 12.
	f.svc.now = func() time.Time { return time.Date(2026, 11, 10, 17, 0, 0, 0, london) }
	week2 := f.board("2026-11-10", "2026-11-10")
	if _, err := f.svc.Complete(ctx, f.hh, week2[0].ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	// Board day Fri 13: week 1 was missed (on its Thursday only), week 2 done on Tuesday.
	m := cal(time.Date(2026, 11, 13, 0, 0, 0, 0, london))
	got = nil
	for _, d := range []string{"2026-11-03", "2026-11-05", "2026-11-10", "2026-11-12"} {
		for _, c := range m[d] {
			got = append(got, d+":"+string(c.Status))
		}
	}
	want = []string{"2026-11-05:missed", "2026-11-10:done"}
	if !slices.Equal(got, want) {
		t.Errorf("after = %v, want %v", got, want)
	}

	// The sheet finds it on either day.
	if _, _, err := f.svc.ChoreOn(ctx, f.hh, mon, week2[0].ChoreID, "2026-11-26", CalPick{}); err != nil {
		t.Errorf("ChoreOn projected Thu: %v", err)
	}
}
