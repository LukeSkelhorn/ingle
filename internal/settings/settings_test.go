package settings

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/valid"
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
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "domus.db"))
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
		got.ScreensaverIdle() != 5*time.Minute {
		t.Fatalf("defaults = %+v, %v", got, err)
	}

	err = svc.Update(ctx, h.ID, mum.ID, Input{HouseholdName: "", Timezone: "Local", DayStartsAt: "25:00", AdminIdleMinutes: 0, ScreensaverIdleMinutes: 121})
	var ve valid.Errors
	if !errors.As(err, &ve) || len(ve) != 5 {
		t.Fatalf("Update(bad) = %v", err)
	}

	if err := svc.Update(ctx, h.ID, mum.ID, Input{HouseholdName: "Smiths", Timezone: "America/New_York", DayStartsAt: "04:30", AdminIdleMinutes: 10, ScreensaverIdleMinutes: 0}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, h.ID)
	if err != nil || got.HouseholdName != "Smiths" || got.Location.String() != "America/New_York" ||
		got.AdminIdle() != 10*time.Minute || got.DayStartsAt != "04:30" || got.ScreensaverIdle() != 0 {
		t.Fatalf("after update = %+v, %v", got, err)
	}
	if loc := svc.Location(ctx, h.ID); loc.String() != "America/New_York" {
		t.Errorf("Location = %v", loc)
	}
}
