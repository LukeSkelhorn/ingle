package lock

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
)

var london, _ = time.LoadLocation("Europe/London")

type fixture struct {
	t       *testing.T
	st      *store.Store
	set     *settings.Service
	svc     *Service
	hh      int64
	mum     int64
	ended   int
	changes int

	mu  sync.Mutex
	now time.Time
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) at(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db := storetest.Open(t, filepath.Join(t.TempDir(), "ingle.db"))
	st := store.New(db)
	h, err := st.EnsureHousehold(ctx, "Home", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	set := settings.New(st)
	if err := set.Ensure(ctx, h.ID, london); err != nil {
		t.Fatal(err)
	}
	mum, err := st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: h.ID, Name: "Mum", Kind: "adult", Colour: "#4A90D9", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Friday 23 October 2026, 08:00.
	f := &fixture{t: t, st: st, set: set, hh: h.ID, mum: mum.ID, now: time.Date(2026, 10, 23, 8, 0, 0, 0, london)}
	f.svc = New(st, set, func(context.Context, int64) error { f.ended++; return nil })
	f.svc.SetClock(f.clock)
	f.svc.OnChange(func(int64) { f.changes++ })
	return f
}

func (f *fixture) settings(days int64, from, until string, idleHours int64) {
	f.t.Helper()
	if err := f.set.Update(f.t.Context(), f.hh, f.mum, settings.Input{
		HouseholdName: "Home", Timezone: "Europe/London", DayStartsAt: "00:00", AdminIdleMinutes: 2,
		ScreensaverIdleMinutes: 5, LockDays: days, LockFrom: from, LockUntil: until, LockIdleHours: idleHours,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) tick(at time.Time) State {
	f.t.Helper()
	f.at(at)
	if err := f.svc.Tick(f.t.Context(), f.hh); err != nil {
		f.t.Fatal(err)
	}
	return f.state()
}

func (f *fixture) state() State {
	f.t.Helper()
	st, err := f.svc.State(f.t.Context(), f.hh)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) verbs() []string {
	f.t.Helper()
	rows, err := f.st.Q.ListRecentActivity(f.t.Context(), dbq.ListRecentActivityParams{HouseholdID: f.hh, Limit: 50})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		if v := rows[i].Verb; len(v) > 6 && v[:6] == "board." {
			out = append(out, v)
		}
	}
	return out
}

func day(d, hh, mm int) time.Time { return time.Date(2026, 10, d, hh, mm, 0, 0, london) }

func TestManualLockAndUnlock(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if f.state().Locked {
		t.Fatal("locked to start with")
	}
	if err := f.svc.Lock(ctx, f.hh, &f.mum, Remote); err != nil {
		t.Fatal(err)
	}
	st := f.state()
	if !st.Locked || st.Reason != Remote || st.By == nil || *st.By != f.mum || !st.Since.Equal(f.clock()) {
		t.Fatalf("after lock: %+v", st)
	}
	// Locking again is a no-op.
	if err := f.svc.Lock(ctx, f.hh, nil, Manual); err != nil {
		t.Fatal(err)
	}
	if f.state().Reason != Remote || f.ended != 1 || f.changes != 1 {
		t.Errorf("second lock changed things: %+v ended=%d changes=%d", f.state(), f.ended, f.changes)
	}
	if err := f.svc.Unlock(ctx, f.hh, nil, ByCode); err != nil {
		t.Fatal(err)
	}
	if f.state().Locked || f.changes != 2 {
		t.Errorf("after unlock: %+v", f.state())
	}
	if got := f.verbs(); len(got) != 2 || got[0] != "board.locked" || got[1] != "board.unlocked_code" {
		t.Errorf("activity = %v", got)
	}
}

func TestSchedule(t *testing.T) {
	f := newFixture(t)
	f.settings(0b0111110, "09:00", "15:00", 0) // weekdays 9–3

	if f.tick(day(23, 8, 59)).Locked {
		t.Fatal("locked before the window")
	}
	if st := f.tick(day(23, 9, 0)); !st.Locked || st.Reason != Schedule {
		t.Fatalf("not locked at 09:00: %+v", st)
	}
	// Someone gets home early and unlocks: it stays unlocked for the rest
	// of the window, even across a restart (a fresh Service).
	if err := f.svc.Unlock(t.Context(), f.hh, nil, ByCode); err != nil {
		t.Fatal(err)
	}
	if f.tick(day(23, 11, 0)).Locked {
		t.Fatal("re-locked within the same window")
	}
	f.svc = New(f.st, f.set, nil)
	f.svc.SetClock(f.clock)
	if f.tick(day(23, 11, 1)).Locked {
		t.Fatal("re-locked after a restart")
	}
	// Monday's window locks; it unlocks itself at 15:00.
	if !f.tick(day(26, 9, 30)).Locked {
		t.Fatal("Monday window didn't lock")
	}
	if f.tick(day(26, 15, 0)).Locked {
		t.Fatal("didn't unlock at the end of the window")
	}
	// A manual lock isn't undone when a window ends.
	f.tick(day(27, 9, 0))
	if err := f.svc.Unlock(t.Context(), f.hh, nil, ByCode); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(t.Context(), f.hh, &f.mum, Remote); err != nil {
		t.Fatal(err)
	}
	if st := f.tick(day(27, 15, 1)); !st.Locked || st.Reason != Remote {
		t.Fatalf("manual lock undone by the schedule: %+v", st)
	}
	// Turning the schedule off releases a schedule lock.
	if err := f.svc.Unlock(t.Context(), f.hh, &f.mum, ByPhone); err != nil {
		t.Fatal(err)
	}
	f.tick(day(28, 9, 0))
	f.settings(0, "09:00", "15:00", 0)
	if f.tick(day(28, 9, 1)).Locked {
		t.Fatal("schedule lock kept after the schedule was turned off")
	}
}

func TestIdle(t *testing.T) {
	f := newFixture(t)
	f.settings(0, "09:00", "15:00", 3)
	start := f.clock()
	f.svc.SetClock(f.clock) // idle counts from now

	if f.tick(start.Add(2 * time.Hour)).Locked {
		t.Fatal("locked too soon")
	}
	f.svc.Touch(f.hh) // someone taps the board at +2h
	if f.tick(start.Add(4*time.Hour + 59*time.Minute)).Locked {
		t.Fatal("Touch didn't hold off the idle lock")
	}
	if st := f.tick(start.Add(5 * time.Hour)); !st.Locked || st.Reason != Idle {
		t.Fatalf("not locked after 3h idle: %+v", st)
	}
	// Unlocking restarts the idle clock.
	if err := f.svc.Unlock(t.Context(), f.hh, nil, ByCode); err != nil {
		t.Fatal(err)
	}
	if f.tick(start.Add(6 * time.Hour)).Locked {
		t.Fatal("re-locked straight after unlocking")
	}
	if got := f.verbs(); len(got) != 2 || got[0] != "board.locked_idle" {
		t.Errorf("activity = %v", got)
	}
}
