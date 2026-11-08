package chores

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// eachChore creates a daily "each of these people" chore worth 2 points and
// 10p to kids and adultPoints to adults, for everyone or for people.
func (f *fixture) eachChore(title string, everyone bool, people []int64, mut ...func(*Input)) dbq.Chore {
	f.t.Helper()
	in := Input{
		Title: title, Mode: ModeEach, Everyone: everyone, PeopleIDs: people,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
		Points: 2, Pence: 10, AdultPoints: 1,
	}
	for _, m := range mut {
		m(&in)
	}
	c, err := f.svc.CreateChore(f.t.Context(), f.hh, f.adult.ID, in, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// cardsTitled is a column's cards with the given title.
func cardsTitled(c Column, title string) []Card {
	var out []Card
	for _, k := range c.Cards {
		if k.Title == title {
			out = append(out, k)
		}
	}
	return out
}

func TestEachChoreValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.CreateChore(t.Context(), f.hh, f.adult.ID, Input{
		Title: "Teeth", Mode: ModeEach, Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
	}, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	var ve valid.Errors
	if !errors.As(err, &ve) || ve["each_ids"] == "" {
		t.Errorf("nobody: err = %v, want an each_ids error", err)
	}
	_, err = f.svc.CreateChore(t.Context(), f.hh, f.adult.ID, Input{
		Title: "Teeth", Mode: ModeEach, Everyone: true, AdultPoints: -1,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02",
	}, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	if !errors.As(err, &ve) || ve["adult_points"] == "" {
		t.Errorf("negative adult points: err = %v, want an adult_points error", err)
	}

	// One person stays an "each" chore (it keeps its adults' points).
	c := f.eachChore("Teeth", false, []int64{f.kid.ID, f.kid.ID})
	if c.Mode != ModeEach || c.ForEveryone != 0 || c.AdultPoints != 1 {
		t.Errorf("chore = %+v", c)
	}
	if got, _ := f.svc.ChorePeople(t.Context(), f.hh, c.ID); !slices.Equal(got, []int64{f.kid.ID}) {
		t.Errorf("people = %v, want just Alfie", got)
	}
	// Everyone keeps no list.
	e := f.eachChore("Bed", true, []int64{f.kid.ID})
	if got, _ := f.svc.ChorePeople(t.Context(), f.hh, e.ID); len(got) != 0 || e.ForEveryone != 1 {
		t.Errorf("everyone: people = %v, for_everyone = %d", got, e.ForEveryone)
	}
}

func TestEachChoreOnTheBoard(t *testing.T) {
	f := newFixture(t)
	f.eachChore("Brush teeth", true, nil)
	f.eachChore("Exercise", false, []int64{f.adult.ID, f.kid.ID})

	b := f.boardAt("2026-11-02")
	for _, name := range []string{"Mum", "Alfie", "Isla"} {
		if n := len(cardsTitled(b.column(name), "Brush teeth")); n != 1 {
			t.Errorf("%s has %d Brush teeth cards, want 1", name, n)
		}
	}
	if n := len(cardsTitled(b.column("Isla"), "Exercise")); n != 0 {
		t.Errorf("Isla has Exercise, want only Mum and Alfie")
	}
	if len(b.Pool) != 0 {
		t.Errorf("pool = %+v, want nothing up for grabs", b.Pool)
	}
	mum, alfie := cardsTitled(b.column("Mum"), "Brush teeth")[0], cardsTitled(b.column("Alfie"), "Brush teeth")[0]
	if mum.Points != 1 || mum.Pence != 0 || alfie.Points != 2 || alfie.Pence != 10 {
		t.Errorf("worth: Mum %d/%dp, Alfie %d/%dp; want 1/0p and 2/10p", mum.Points, mum.Pence, alfie.Points, alfie.Pence)
	}

	// Each does their own: Alfie's can't be done as Isla, and doing his
	// leaves everyone else's.
	if _, err := f.svc.Complete(t.Context(), f.hh, alfie.OccurrenceID, f.kid2.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("Isla doing Alfie's: %v, want ErrNotYours", err)
	}
	ca, err := f.svc.Complete(t.Context(), f.hh, alfie.OccurrenceID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := f.svc.Complete(t.Context(), f.hh, mum.OccurrenceID, f.adult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ca.Points != 2 || ca.Pence != 10 || cm.Points != 1 || cm.Pence != 0 {
		t.Errorf("completions: Alfie %+v, Mum %+v", ca, cm)
	}
	if bal := f.balance(f.adult); bal.Points != 1 || bal.Pence != 0 {
		t.Errorf("Mum's balance = %+v, want 1 point and no money", bal)
	}
	b = f.boardAt("2026-11-02")
	if c := cardsTitled(b.column("Isla"), "Brush teeth"); len(c) != 1 || c[0].Done {
		t.Errorf("Isla's = %+v, want still to do", c)
	}
	if c := cardsTitled(b.column("Alfie"), "Brush teeth"); len(c) != 1 || !c[0].Done {
		t.Errorf("Alfie's = %+v, want done", c)
	}

	// The next day, yesterday's undone copies are each replaced by that
	// person's new one, not piled up or swallowed by someone else's.
	f.at("2026-11-03", "09:00")
	b = f.boardAt("2026-11-03")
	for _, name := range []string{"Mum", "Alfie", "Isla"} {
		c := cardsTitled(b.column(name), "Brush teeth")
		if len(c) != 1 || c[0].DueOn != "2026-11-03" {
			t.Errorf("%s next day = %+v, want just today's", name, c)
		}
	}
}

func TestEachChoreWeeklyOverduePerPerson(t *testing.T) {
	f := newFixture(t)
	weekly := func(in *Input) { in.Repeat = Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Monday}} }
	f.eachChore("Tidy room", false, []int64{f.kid.ID, f.kid2.ID}, weekly)
	b := f.boardAt("2026-11-02")
	isla := cardsTitled(b.column("Isla"), "Tidy room")[0]
	if _, err := f.svc.Complete(t.Context(), f.hh, isla.OccurrenceID, f.kid2.ID); err != nil {
		t.Fatal(err)
	}
	// Wednesday: Alfie's Monday one is overdue; Isla's is done and gone.
	f.at("2026-11-04", "09:00")
	b = f.boardAt("2026-11-04")
	if c := cardsTitled(b.column("Alfie"), "Tidy room"); len(c) != 1 || !c[0].Overdue {
		t.Errorf("Alfie = %+v, want his overdue one", c)
	}
	if c := cardsTitled(b.column("Isla"), "Tidy room"); len(c) != 0 {
		t.Errorf("Isla = %+v, want nothing", c)
	}
}

func TestEachChoreEveryoneFollowsTheHousehold(t *testing.T) {
	f := newFixture(t)
	f.eachChore("Brush teeth", true, nil)
	f.eachChore("Exercise", false, []int64{f.kid.ID})
	f.person("Dad", "adult")
	b := f.boardAt("2026-11-02") // materialises again, as the hourly run does
	if n := len(cardsTitled(b.column("Dad"), "Brush teeth")); n != 1 {
		t.Errorf("Dad has %d Brush teeth cards, want 1", n)
	}
	if n := len(cardsTitled(b.column("Dad"), "Exercise")); n != 0 {
		t.Errorf("Dad has Exercise, want only Alfie")
	}
}

func TestEachChoreTimesAndApproval(t *testing.T) {
	f := newFixture(t)
	f.eachChore("Brush teeth", false, []int64{f.adult.ID, f.kid.ID}, func(in *Input) {
		in.DueTimes, in.RequiresApproval = []string{"07:30", "19:30"}, true
	})
	b := f.boardAt("2026-11-02")
	if got := cardTimes(Column{Cards: cardsTitled(b.column("Alfie"), "Brush teeth")}); !slices.Equal(got, []string{"07:30", "19:30"}) {
		t.Errorf("Alfie's times = %q", got)
	}
	if n := len(cardsTitled(b.column("Mum"), "Brush teeth")); n != 2 {
		t.Errorf("Mum has %d, want 2", n)
	}
	c, err := f.svc.Complete(t.Context(), f.hh, cardsTitled(b.column("Mum"), "Brush teeth")[0].OccurrenceID, f.adult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusPending || c.Points != 1 || c.Pence != 0 {
		t.Errorf("Mum's completion = %+v, want pending, 1 point", c)
	}
	if err := f.svc.Approve(t.Context(), f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if bal := f.balance(f.adult); bal.Points != 1 || bal.Pence != 0 {
		t.Errorf("Mum's balance = %+v", bal)
	}
}

func TestEachChoreEditPeople(t *testing.T) {
	f := newFixture(t)
	c := f.eachChore("Exercise", false, []int64{f.kid.ID, f.kid2.ID})
	b := f.boardAt("2026-11-02")
	alfie := cardsTitled(b.column("Alfie"), "Exercise")[0]
	if _, err := f.svc.Complete(t.Context(), f.hh, alfie.OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	// Isla's taken off and Mum's added: Isla's open copy goes, Alfie's done
	// one stays.
	if _, err := f.svc.UpdateChore(t.Context(), f.hh, f.adult.ID, c.ID, Input{
		Title: "Exercise", Mode: ModeEach, PeopleIDs: []int64{f.kid.ID, f.adult.ID},
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02", Points: 2, Pence: 10, AdultPoints: 1,
	}, time.Date(2026, 11, 2, 0, 0, 0, 0, london)); err != nil {
		t.Fatal(err)
	}
	b = f.boardAt("2026-11-02")
	if n := len(cardsTitled(b.column("Isla"), "Exercise")); n != 0 {
		t.Errorf("Isla still has %d", n)
	}
	if k := cardsTitled(b.column("Alfie"), "Exercise"); len(k) != 1 || !k[0].Done {
		t.Errorf("Alfie = %+v, want his done one", k)
	}
	if n := len(cardsTitled(b.column("Mum"), "Exercise")); n != 1 {
		t.Errorf("Mum has %d, want 1", n)
	}
}

func TestEachChoreCalendarAndMissed(t *testing.T) {
	f := newFixture(t)
	f.eachChore("Brush teeth", false, []int64{f.adult.ID, f.kid.ID}, func(in *Input) {
		in.DueTimes, in.OnTheDay = []string{"07:30"}, true
	})
	// A day past the materialised window is projected per person.
	cal := f.calendar("2026-11-02", "2026-12-20", "2026-12-20")
	var got []string
	for _, c := range cal["2026-12-20"] {
		got = append(got, f.name(c.PersonID)+":"+strconv.FormatInt(c.Points, 10))
	}
	slices.Sort(got)
	if want := []string{"Alfie:2", "Mum:1"}; !slices.Equal(got, want) {
		t.Errorf("projected = %q, want %q", got, want)
	}
	// Materialised days too, and ChoreOn picks the person asked for.
	d := time.Date(2026, 11, 2, 0, 0, 0, 0, london)
	f.boardAt("2026-11-02")
	if n := len(f.calendar("2026-11-02", "2026-11-03", "2026-11-03")["2026-11-03"]); n != 2 {
		t.Errorf("materialised day has %d, want 2", n)
	}
	c, _, err := f.svc.ChoreOn(t.Context(), f.hh, d, cal["2026-12-20"][0].ChoreID, "2026-11-03", CalPick{PersonID: f.kid.ID})
	if err != nil || c.PersonID == nil || *c.PersonID != f.kid.ID || c.Points != 2 {
		t.Errorf("ChoreOn Alfie = %+v, %v", c, err)
	}

	f.at("2026-11-02", "08:00")
	missed, err := f.svc.Missed(t.Context(), f.hh, d)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, m := range missed {
		got = append(got, m.Person.Name+":"+strconv.FormatInt(m.Points, 10))
	}
	slices.Sort(got)
	if want := []string{"Alfie:2", "Mum:1"}; !slices.Equal(got, want) {
		t.Errorf("missed = %q, want %q", got, want)
	}
}

func (f *fixture) name(id *int64) string {
	if id == nil {
		return "nobody"
	}
	for _, p := range []dbq.Person{f.adult, f.kid, f.kid2} {
		if p.ID == *id {
			return p.Name
		}
	}
	return "?"
}
