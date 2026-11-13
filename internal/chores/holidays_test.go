package chores

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func (f *fixture) day(date string) time.Time {
	f.t.Helper()
	d, err := time.ParseInLocation(DateLayout, date, london)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

// holiday adds a holiday (for everyone when person is nil), on boardDay.
func (f *fixture) holiday(person *int64, from, to, boardDay string) int64 {
	f.t.Helper()
	id, err := f.svc.AddHoliday(f.t.Context(), f.hh, f.adult.ID, HolidayInput{PersonID: person, From: from, To: to}, f.day(boardDay))
	if err != nil {
		f.t.Fatalf("AddHoliday: %v", err)
	}
	return id
}

func choreDates(rows []dbq.ListBoardRow, title string) []string {
	var out []string
	for _, r := range rows {
		if r.Title == title {
			out = append(out, r.DueOn)
		}
	}
	return out
}

func TestHolidayForEveryone(t *testing.T) {
	f := newFixture(t)
	daily, weekly := "FREQ=DAILY", "FREQ=WEEKLY;BYDAY=TU"
	f.chore("Feed cat", choreOpts{assignee: &f.kid.ID, rrule: &daily})
	f.chore("Hoover", choreOpts{assignee: &f.kid.ID, rrule: &weekly})
	f.chore("Car wash", choreOpts{rrule: &daily}) // up for grabs
	f.materialise("2026-11-02", Window)

	// Away Wed 4 to Fri 6 Nov, added on Mon 2.
	f.holiday(nil, "2026-11-04", "2026-11-06", "2026-11-02")

	got := choreDates(f.board("2026-11-02", "2026-11-08"), "Feed cat")
	want := []string{"2026-11-02", "2026-11-03", "2026-11-07", "2026-11-08"}
	if !slices.Equal(got, want) {
		t.Fatalf("Feed cat due %v, want %v", got, want)
	}

	// During: nothing on the board, and the banner.
	b := f.boardAt("2026-11-05")
	if len(b.Pool) != 0 || len(b.column("Alfie").Cards) != 0 {
		t.Fatalf("board on holiday: pool %v, Alfie %v", titles(b.Pool), titles(b.column("Alfie").Cards))
	}
	if b.Holiday == nil || b.Holiday.Until("2026-11-05") != "until Friday" {
		t.Fatalf("Holiday = %+v, want one until Friday", b.Holiday)
	}

	// The day after: today's chores, and Tuesday's missed Hoover is cleared
	// (it would otherwise be overdue until next Tuesday).
	b = f.boardAt("2026-11-07")
	if b.Holiday != nil {
		t.Fatalf("Holiday = %+v after it ended", b.Holiday)
	}
	if got, want := titles(b.column("Alfie").Cards), []string{"Feed cat@2026-11-07"}; !slices.Equal(got, want) {
		t.Fatalf("Alfie = %v, want %v", got, want)
	}
	if got, want := titles(b.Pool), []string{"Car wash@2026-11-07"}; !slices.Equal(got, want) {
		t.Fatalf("pool = %v, want %v", got, want)
	}
}

func TestHolidayForOnePerson(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	daily := "FREQ=DAILY"
	f.chore("Feed cat", choreOpts{assignee: &f.kid.ID, rrule: &daily})
	f.eachChore("Teeth", false, []int64{f.kid.ID, f.kid2.ID})
	f.sharedChore("Dishes", []int64{f.kid.ID, f.kid2.ID})
	f.sharedChore("Walk dog", []int64{f.kid.ID, f.kid2.ID})
	f.chore("Car wash", choreOpts{startsOn: "2026-11-05"}) // one-off, up for grabs
	f.materialise("2026-11-02", Window)
	for _, r := range f.board("2026-11-05", "2026-11-05") {
		if r.Title == "Car wash" {
			if err := f.svc.Claim(ctx, f.hh, r.ID, f.kid.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Alfie's at camp Wed 4 to Fri 6; Isla joins him on Thursday.
	f.holiday(&f.kid.ID, "2026-11-04", "2026-11-06", "2026-11-02")
	isla := f.holiday(&f.kid2.ID, "2026-11-05", "2026-11-05", "2026-11-02")

	b := f.boardAt("2026-11-04")
	alfie := b.column("Alfie")
	if alfie.Away == nil || alfie.Away.Until("2026-11-04") != "until Friday" || len(alfie.Cards) != 0 {
		t.Fatalf("Alfie: Away %+v, cards %v", alfie.Away, titles(alfie.Cards))
	}
	if b.Holiday != nil {
		t.Fatalf("Holiday = %+v, want nil (only Alfie's away)", b.Holiday)
	}
	// Isla carries on with the shared chores; her own copy of Teeth stays.
	got := titles(b.column("Isla").Cards)
	want := []string{"Dishes@2026-11-04", "Teeth@2026-11-04", "Walk dog@2026-11-04"}
	if !slices.Equal(got, want) {
		t.Fatalf("Isla = %v, want %v", got, want)
	}

	// Thursday both are away: the shared chores pause, and the car wash
	// Alfie claimed is back up for grabs.
	b = f.boardAt("2026-11-05")
	if n := len(b.column("Isla").Cards) + len(b.column("Alfie").Cards); n != 0 {
		t.Fatalf("Isla %v, Alfie %v, want nothing", titles(b.column("Isla").Cards), titles(b.column("Alfie").Cards))
	}
	if got, want := titles(b.Pool), []string{"Car wash@2026-11-05"}; !slices.Equal(got, want) {
		t.Fatalf("pool = %v, want %v", got, want)
	}

	// Removing Isla's holiday brings her Thursday back.
	if err := f.svc.RemoveHoliday(ctx, f.hh, f.adult.ID, isla, f.day("2026-11-02")); err != nil {
		t.Fatal(err)
	}
	if got, want := titles(f.boardAt("2026-11-05").column("Isla").Cards), []string{"Dishes@2026-11-05", "Teeth@2026-11-05", "Walk dog@2026-11-05"}; !slices.Equal(got, want) {
		t.Fatalf("Isla after removing her holiday = %v, want %v", got, want)
	}
}

func TestHolidayFlexibleDays(t *testing.T) {
	f := newFixture(t)
	rule := "FREQ=WEEKLY;BYDAY=MO,WE,FR"
	f.chore("Hoover", choreOpts{assignee: &f.kid.ID, rrule: &rule, anyDay: true})
	// Away Wed to Fri: Monday's still free, so that week stays. Away the
	// whole of the next week, so it's paused.
	f.holiday(&f.kid.ID, "2026-11-04", "2026-11-06", "2026-11-02")
	f.holiday(&f.kid.ID, "2026-11-09", "2026-11-15", "2026-11-02")
	f.materialise("2026-11-02", Window)
	got := windows(f.board("2026-11-02", "2026-11-15"))
	if want := []string{"2026-11-02..2026-11-06"}; !slices.Equal(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestEndHolidayNow(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	daily := "FREQ=DAILY"
	f.chore("Feed cat", choreOpts{assignee: &f.kid.ID, rrule: &daily})
	id := f.holiday(nil, "2026-11-01", "2026-11-10", "2026-11-02")

	// Back early, on Thu 5: today's chores come back.
	if err := f.svc.EndHoliday(ctx, f.hh, f.adult.ID, id, f.day("2026-11-05")); err != nil {
		t.Fatal(err)
	}
	b := f.boardAt("2026-11-05")
	if b.Holiday != nil {
		t.Fatalf("Holiday = %+v after ending it", b.Holiday)
	}
	if got, want := titles(b.column("Alfie").Cards), []string{"Feed cat@2026-11-05"}; !slices.Equal(got, want) {
		t.Fatalf("Alfie = %v, want %v", got, want)
	}
	hs, err := f.svc.Holidays(ctx, f.hh, f.day("2026-11-01"))
	if err != nil || len(hs) != 1 || hs[0].EndsOn != "2026-11-04" {
		t.Fatalf("Holidays = %+v, %v; want one ending Wed 4", hs, err)
	}

	// Ending one that hasn't started yet removes it.
	later := f.holiday(nil, "2026-11-20", "2026-11-22", "2026-11-05")
	if err := f.svc.EndHoliday(ctx, f.hh, f.adult.ID, later, f.day("2026-11-05")); err != nil {
		t.Fatal(err)
	}
	if hs, _ := f.svc.Holidays(ctx, f.hh, f.day("2026-11-05")); len(hs) != 0 {
		t.Fatalf("Holidays = %+v, want none", hs)
	}
}

func TestHolidayMissedAndCalendar(t *testing.T) {
	f := newFixture(t)
	daily := "FREQ=DAILY"
	f.chore("Teeth", choreOpts{assignee: &f.kid.ID, rrule: &daily, dueTime: ptr("07:00"), onTheDay: true})
	f.materialise("2026-11-02", Window)
	f.holiday(nil, "2026-11-04", "2026-11-06", "2026-11-02")
	f.holiday(nil, "2026-11-20", "2026-11-20", "2026-11-02") // past the materialised window

	// Tuesday's missed Teeth is cleared by the holiday.
	f.at("2026-11-05", "12:00")
	missed, err := f.svc.Missed(t.Context(), f.hh, f.day("2026-11-05"))
	if err != nil || len(missed) != 0 {
		t.Fatalf("Missed = %+v, %v; want none", missed, err)
	}

	f.at("2026-11-02", "09:00")
	cal := f.calendar("2026-11-02", "2026-11-02", "2026-11-21")
	for _, d := range []string{"2026-11-04", "2026-11-05", "2026-11-06", "2026-11-20"} {
		if len(cal[d]) != 0 {
			t.Errorf("calendar %s = %s, want nothing", d, calTitles(cal[d]))
		}
	}
	for _, d := range []string{"2026-11-03", "2026-11-07", "2026-11-21"} {
		if len(cal[d]) != 1 {
			t.Errorf("calendar %s = %s, want Teeth", d, calTitles(cal[d]))
		}
	}
}

func TestAddHolidayValidation(t *testing.T) {
	f := newFixture(t)
	other := int64(9999)
	tests := []struct {
		in    HolidayInput
		field string
	}{
		{HolidayInput{From: "", To: "2026-11-05"}, "holiday_from"},
		{HolidayInput{From: "2026-11-05", To: "2026-11-04"}, "holiday_to"},
		{HolidayInput{From: "2026-10-20", To: "2026-11-01"}, "holiday_to"}, // already over
		{HolidayInput{From: "2026-11-05", To: "2027-12-01"}, "holiday_to"}, // too long
		{HolidayInput{PersonID: &other, From: "2026-11-05", To: "2026-11-06"}, "holiday_person"},
	}
	for _, tt := range tests {
		_, err := f.svc.AddHoliday(t.Context(), f.hh, f.adult.ID, tt.in, f.day("2026-11-02"))
		var errs valid.Errors
		if !errors.As(err, &errs) || errs[tt.field] == "" {
			t.Errorf("AddHoliday(%+v) = %v, want an error on %s", tt.in, err, tt.field)
		}
	}
}

func TestHolidayLabels(t *testing.T) {
	h := Holiday{StartsOn: "2026-08-01", EndsOn: "2026-08-10"}
	tests := []struct{ today, dates, until string }{
		{"2026-07-20", "Sat 1 Aug – Mon 10 Aug", "until Mon 10 Aug"},
		{"2026-08-04", "Sat 1 Aug – Mon 10 Aug", "until Monday"},
		{"2026-08-10", "Sat 1 Aug – Mon 10 Aug", "today"},
		{"2025-12-01", "Sat 1 Aug 2026 – Mon 10 Aug 2026", "until Mon 10 Aug 2026"},
	}
	for _, tt := range tests {
		if got := h.Dates(tt.today); got != tt.dates {
			t.Errorf("Dates(%s) = %q, want %q", tt.today, got, tt.dates)
		}
		if got := h.Until(tt.today); got != tt.until {
			t.Errorf("Until(%s) = %q, want %q", tt.today, got, tt.until)
		}
	}
}
