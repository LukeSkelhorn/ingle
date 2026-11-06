package settings

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func TestBoardDay(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Location: london, dayStart: 4 * time.Hour}
	tests := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 7, 1, 3, 59, 0, 0, london), "2026-06-30"},
		{time.Date(2026, 7, 1, 4, 0, 0, 0, london), "2026-07-01"},
		{time.Date(2026, 7, 1, 23, 59, 0, 0, london), "2026-07-01"},
		// 25 Oct 2026, clocks go back at 02:00 BST: 03:30 GMT is before reset.
		{time.Date(2026, 10, 25, 3, 30, 0, 0, london), "2026-10-24"},
		{time.Date(2026, 10, 25, 4, 30, 0, 0, london), "2026-10-25"},
		// Input in UTC is converted: 02:30 UTC on 1 July is 03:30 BST.
		{time.Date(2026, 7, 1, 2, 30, 0, 0, time.UTC), "2026-06-30"},
	}
	for _, tt := range tests {
		if got := s.Today(tt.at); got != tt.want {
			t.Errorf("Today(%v) = %s, want %s", tt.at, got, tt.want)
		}
	}
	midnight := Settings{Location: london}
	if got := midnight.Today(time.Date(2026, 7, 1, 0, 1, 0, 0, london)); got != "2026-07-01" {
		t.Errorf("midnight reset Today = %s", got)
	}
}

func TestServiceUpdate(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	h, err := st.EnsureHousehold(ctx, "Home", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mum, err := st.Q.CreatePerson(ctx, createPerson(h.ID))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st)
	london, _ := time.LoadLocation("Europe/London")
	if err := svc.Ensure(ctx, h.ID, london); err != nil {
		t.Fatal(err)
	}
	// Ensure doesn't override a stored timezone.
	if err := svc.Ensure(ctx, h.ID, time.UTC); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, h.ID)
	if err != nil || got.Location.String() != "Europe/London" || got.AdminIdleMinutes != 2 || got.DayStartsAt != "00:00" ||
		got.ScreensaverIdle() != 5*time.Minute || got.CalendarIdle() != 2*time.Minute {
		t.Fatalf("defaults = %+v, %v", got, err)
	}

	err = svc.Update(ctx, h.ID, mum.ID, Input{
		HouseholdName: "", Timezone: "Local", DayStartsAt: "25:00", AdminIdleMinutes: 0, ScreensaverIdleMinutes: 121, CalendarIdleMinutes: 61,
		LockDays: 128, LockFrom: "9am", LockUntil: "15:00", LockIdleHours: 73,
	})
	var ve valid.Errors
	if !errors.As(err, &ve) || len(ve) != 9 {
		t.Fatalf("Update(bad) = %v", err)
	}

	if err := svc.Update(ctx, h.ID, mum.ID, Input{
		HouseholdName: "Smiths", Timezone: "America/New_York", DayStartsAt: "04:30", AdminIdleMinutes: 10, ScreensaverIdleMinutes: 0, CalendarIdleMinutes: 7,
		LockDays: 0b0111110, LockFrom: "08:45", LockUntil: "15:15", LockIdleHours: 12,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, h.ID)
	if err != nil || got.HouseholdName != "Smiths" || got.Location.String() != "America/New_York" ||
		got.AdminIdle() != 10*time.Minute || got.DayStartsAt != "04:30" || got.ScreensaverIdle() != 0 || got.CalendarIdle() != 7*time.Minute ||
		got.LockDays != 0b0111110 || got.LockFrom != "08:45" || got.LockUntil != "15:15" || got.LockIdle() != 12*time.Hour {
		t.Fatalf("after update = %+v, %v", got, err)
	}
	if loc := svc.Location(ctx, h.ID); loc.String() != "America/New_York" {
		t.Errorf("Location = %v", loc)
	}
}

func TestLockWindow(t *testing.T) {
	london, _ := time.LoadLocation("Europe/London")
	mk := func(days int64, from, until string) Settings {
		f, _ := ParseClock(from)
		u, _ := ParseClock(until)
		return Settings{Location: london, LockDays: days, LockFrom: from, LockUntil: until, lockFrom: f, lockUntil: u}
	}
	at := func(d, hh, mm int) time.Time { return time.Date(2026, 10, d, hh, mm, 0, 0, london) }
	const weekdays = 0b0111110
	// 2026-10-23 is a Friday; the clocks go back at 02:00 on Sunday 25th.
	for _, tc := range []struct {
		name       string
		s          Settings
		now        time.Time
		start, end time.Time
		in         bool
	}{
		{"school day, inside", mk(weekdays, "09:00", "15:00"), at(23, 10, 0), at(23, 9, 0), at(23, 15, 0), true},
		{"school day, before", mk(weekdays, "09:00", "15:00"), at(23, 8, 59), at(22, 9, 0), at(22, 15, 0), false},
		{"exactly at start", mk(weekdays, "09:00", "15:00"), at(23, 9, 0), at(23, 9, 0), at(23, 15, 0), true},
		{"exactly at end", mk(weekdays, "09:00", "15:00"), at(23, 15, 0), at(23, 9, 0), at(23, 15, 0), false},
		{"weekend skips back to Friday", mk(weekdays, "09:00", "15:00"), at(26, 8, 0), at(23, 9, 0), at(23, 15, 0), false},
		{"overnight across the clock change", mk(1<<6, "22:00", "07:00"), at(25, 1, 30), at(24, 22, 0), at(25, 7, 0), true},
		{"same weekday last week", mk(1<<5, "09:00", "15:00"), at(23, 8, 0), at(16, 9, 0), at(16, 15, 0), false},
	} {
		start, end, in, ok := tc.s.LockWindow(tc.now)
		if !ok || !start.Equal(tc.start) || !end.Equal(tc.end) || in != tc.in {
			t.Errorf("%s: got %v–%v in=%v ok=%v; want %v–%v in=%v", tc.name, start, end, in, ok, tc.start, tc.end, tc.in)
		}
	}
	if d := at(25, 7, 0).Sub(at(24, 22, 0)); d != 10*time.Hour {
		t.Errorf("overnight window across the change lasts %v", d)
	}
	if _, _, _, ok := mk(0, "09:00", "15:00").LockWindow(at(23, 10, 0)); ok {
		t.Error("schedule with no days is on")
	}
}
