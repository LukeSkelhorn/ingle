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

func (f *fixture) countdown(in CountdownInput) int64 {
	f.t.Helper()
	id, err := f.svc.AddCountdown(f.t.Context(), f.hh, f.adult.ID, in, f.day("2026-11-02"))
	if err != nil {
		f.t.Fatalf("AddCountdown: %v", err)
	}
	return id
}

func (f *fixture) countdowns(boardDay string, withPassed bool) []Countdown {
	f.t.Helper()
	cs, err := f.svc.Countdowns(f.t.Context(), f.hh, f.day(boardDay), withPassed)
	if err != nil {
		f.t.Fatal(err)
	}
	return cs
}

func countdownLabels(cs []Countdown) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Title+" · "+c.Label())
	}
	return out
}

func TestCountdowns(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.countdown(CountdownInput{Title: "Christmas", Emoji: "🎄", OnDate: "2025-12-25", Yearly: true})
	f.countdown(CountdownInput{Title: "Holiday to Spain", OnDate: "2026-11-05", PeopleIDs: []int64{f.kid.ID, f.adult.ID, f.kid.ID}})
	f.countdown(CountdownInput{Title: "Bonfire night", OnDate: "2026-11-05"})
	bday := "11-03"
	year := int64(2017)
	if err := f.svc.SetBirthday(ctx, f.hh, f.kid.ID, &bday, &year); err != nil {
		t.Fatal(err)
	}

	got := countdownLabels(f.countdowns("2026-11-02", false))
	want := []string{"Alfie's birthday · Tomorrow", "Bonfire night · 3 days", "Holiday to Spain · 3 days", "Christmas · 7 weeks"}
	if !slices.Equal(got, want) {
		t.Fatalf("countdowns = %q, want %q", got, want)
	}
	cs := f.countdowns("2026-11-02", false)
	if cs[0].Turns != 9 || !cs[0].Birthday || len(cs[0].People) != 1 || cs[0].Person().ID != f.kid.ID {
		t.Errorf("birthday = %+v, want Alfie turning 9", cs[0])
	}
	if len(cs[2].People) != 2 || !slices.ContainsFunc(cs[2].People, func(p dbq.Person) bool { return p.ID == f.kid.ID }) {
		t.Errorf("Spain is for %v, want Alfie and a grown-up", cs[2].People)
	}
	if len(cs[3].People) != 0 {
		t.Errorf("Christmas is for %v, want nobody", cs[3].People)
	}
	if cs[3].Next != "2026-12-25" || cs[3].When() != "Fri 25 Dec" {
		t.Errorf("Christmas next = %s (%s)", cs[3].Next, cs[3].When())
	}

	got = countdownLabels(f.countdowns("2026-11-06", false))
	want = []string{"Christmas · 7 weeks", "Alfie's birthday · 11 months"}
	if !slices.Equal(got, want) {
		t.Fatalf("after = %q, want %q", got, want)
	}
	cs = f.countdowns("2026-11-06", true)
	if n := len(cs); n != 4 || !cs[2].Passed() || !cs[3].Passed() {
		t.Fatalf("with passed = %q", countdownLabels(cs))
	}
	if b := f.countdowns("2026-11-06", false)[1]; b.Turns != 10 || b.Next != "2027-11-03" || b.When() != "Wed 3 Nov 2027" {
		t.Errorf("next birthday = %+v", b)
	}
	if c := f.countdowns("2026-12-25", false)[0]; c.Label() != "Today!" || c.Sentence() != "Christmas is today!" {
		t.Errorf("on the day = %q / %q", c.Label(), c.Sentence())
	}
}

func TestCountdownsOn(t *testing.T) {
	f := newFixture(t)
	f.countdown(CountdownInput{Title: "Christmas", Emoji: "🎄", OnDate: "2025-12-25", Yearly: true})
	f.countdown(CountdownInput{Title: "Bonfire night", OnDate: "2026-11-05"})
	bday, year := "01-02", int64(2017)
	if err := f.svc.SetBirthday(t.Context(), f.hh, f.kid.ID, &bday, &year); err != nil {
		t.Fatal(err)
	}
	on := func(first, last string) map[string][]string {
		t.Helper()
		got, err := f.svc.CountdownsOn(t.Context(), f.hh, f.day(first), f.day(last))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for d, cs := range got {
			for _, c := range cs {
				s := c.Title
				if c.Turns > 0 {
					s += " " + strconv.Itoa(c.Turns)
				}
				out[d] = append(out[d], s)
			}
		}
		return out
	}
	got := on("2026-12-21", "2027-01-31")
	if len(got) != 2 || !slices.Equal(got["2026-12-25"], []string{"Christmas"}) || !slices.Equal(got["2027-01-02"], []string{"Alfie's birthday 10"}) {
		t.Errorf("Dec–Jan = %v", got)
	}
	if got := on("2026-11-02", "2026-11-08"); !slices.Equal(got["2026-11-05"], []string{"Bonfire night"}) || len(got) != 1 {
		t.Errorf("Bonfire week = %v", got)
	}
	if got := on("2027-11-01", "2027-11-30"); len(got) != 0 {
		t.Errorf("Nov 2027 = %v, want nothing", got)
	}
}

func TestCountdownLeapDay(t *testing.T) {
	f := newFixture(t)
	md := "02-29"
	if err := f.svc.SetBirthday(t.Context(), f.hh, f.kid.ID, &md, nil); err != nil {
		t.Fatal(err)
	}
	if c := f.countdowns("2027-01-01", false)[0]; c.Next != "2027-02-28" || c.Turns != 0 {
		t.Errorf("2027: %+v, want 28 Feb and no age", c)
	}
	if c := f.countdowns("2028-01-01", false)[0]; c.Next != "2028-02-29" {
		t.Errorf("2028: %s, want 29 Feb", c.Next)
	}
}

func TestCountdownLabels(t *testing.T) {
	for _, c := range []struct {
		days int
		next string
		want string
	}{
		{0, "2026-11-02", "Today!"},
		{1, "2026-11-03", "Tomorrow"},
		{13, "2026-11-15", "13 days"},
		{14, "2026-11-16", "2 weeks"},
		{90, "2027-01-31", "12 weeks"},
		{91, "2027-02-01", "2 months"},
		{364, "2027-11-01", "11 months"},
	} {
		got := Countdown{Days: c.days, Next: c.next, today: "2026-11-02"}.Label()
		if got != c.want {
			t.Errorf("%d days: %q, want %q", c.days, got, c.want)
		}
	}
}

func TestCountdownChecks(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.day("2026-11-02")
	other := int64(9999)
	for _, c := range []struct {
		in    CountdownInput
		field string
	}{
		{CountdownInput{Title: " ", OnDate: "2026-12-01"}, "title"},
		{CountdownInput{Title: "This title is far too long to fit on the board's countdown strip, really", OnDate: "2026-12-01"}, "title"},
		{CountdownInput{Title: "Trip", OnDate: "2026-02-30"}, "on_date"},
		{CountdownInput{Title: "Trip", OnDate: "2026-11-01"}, "on_date"},
		{CountdownInput{Title: "Trip", OnDate: "2060-01-01"}, "on_date"},
		{CountdownInput{Title: "Trip", OnDate: "2026-12-01", Emoji: "ab"}, "emoji"},
		{CountdownInput{Title: "Trip", OnDate: "2026-12-01", PeopleIDs: []int64{f.kid.ID, other}}, "people_ids"},
	} {
		_, err := f.svc.AddCountdown(ctx, f.hh, f.adult.ID, c.in, day)
		var ve valid.Errors
		if !errors.As(err, &ve) || ve[c.field] == "" {
			t.Errorf("%+v: err = %v, want a %s error", c.in, err, c.field)
		}
	}
	if _, err := f.svc.AddCountdown(ctx, f.hh, f.adult.ID, CountdownInput{Title: "Wedding anniversary", OnDate: "2012-06-09", Yearly: true}, day); err != nil {
		t.Errorf("past yearly: %v", err)
	}

	id := f.countdown(CountdownInput{Title: "Trip", OnDate: "2026-12-01"})
	if err := f.svc.UpdateCountdown(ctx, f.hh, f.adult.ID, id, CountdownInput{Title: "Big trip", Emoji: "✈️", OnDate: "2026-12-02"}, day); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.Countdown(ctx, f.hh, id)
	if err != nil || c.Title != "Big trip" || c.OnDate != "2026-12-02" || c.Emoji != "✈️" {
		t.Fatalf("after update: %+v %v", c, err)
	}
	if err := f.svc.RemoveCountdown(ctx, f.hh, f.adult.ID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Countdown(ctx, f.hh, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("after remove: %v", err)
	}
	if err := f.svc.RemoveCountdown(ctx, f.hh, f.adult.ID, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove again: %v", err)
	}
}

func TestBirthdayParse(t *testing.T) {
	today := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		b         Birthday
		md        string
		year      int64
		wantError bool
	}{
		{Birthday{}, "", 0, false},
		{Birthday{Day: "3", Month: "11"}, "11-03", 0, false},
		{Birthday{Day: "3", Month: "11", Year: "2017"}, "11-03", 2017, false},
		{Birthday{Day: "29", Month: "2"}, "02-29", 0, false},
		{Birthday{Day: "29", Month: "2", Year: "2019"}, "", 0, true},
		{Birthday{Day: "31", Month: "4"}, "", 0, true},
		{Birthday{Day: "3"}, "", 0, true},
		{Birthday{Year: "2017"}, "", 0, true},
		{Birthday{Day: "3", Month: "11", Year: "17"}, "", 0, true},
		{Birthday{Day: "3", Month: "12", Year: "2026"}, "", 0, true},
	} {
		md, year, msg := c.b.Parse(today)
		if (msg != "") != c.wantError {
			t.Errorf("%+v: msg %q", c.b, msg)
			continue
		}
		if c.wantError {
			continue
		}
		if gotMD := deref(md); gotMD != c.md {
			t.Errorf("%+v: md %q, want %q", c.b, gotMD, c.md)
		}
		if gotY := derefInt(year); gotY != c.year {
			t.Errorf("%+v: year %d, want %d", c.b, gotY, c.year)
		}
	}
	md, y := "02-09", int64(2015)
	p := dbq.Person{Birthday: &md, BirthYear: &y}
	if b := BirthdayOf(p); b != (Birthday{Day: "9", Month: "2", Year: "2015"}) {
		t.Errorf("BirthdayOf = %+v", b)
	}
}

func derefInt(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
