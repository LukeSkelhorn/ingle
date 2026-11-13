// Package settings stores per-household preferences (name, timezone, when
// the board day rolls over, admin, screensaver and calendar idle timeouts) and caches them so hot paths
// such as session checks and board rendering don't hit the database.
package settings

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// DateLayout is the storage format for local calendar dates.
const DateLayout = "2006-01-02"

// Settings is the resolved settings for one household.
type Settings struct {
	HouseholdID      int64
	HouseholdName    string
	Location         *time.Location
	DayStartsAt      string // "HH:MM"
	AdminIdleMinutes int64
	// ScreensaverIdleMinutes is how long a household display waits before
	// showing the screensaver; 0 means never.
	ScreensaverIdleMinutes int64
	// CalendarIdleMinutes is how long a display shows the board calendar,
	// untouched, before going back to the chores; 0 means never.
	CalendarIdleMinutes int64
	// CalendarChores is whether the board calendar shows chores as well as
	// events by default; the board's Chores button overrides it for a while.
	CalendarChores bool
	// Lock schedule: on the weekdays in LockDays (bit 0 = Sunday) the
	// displays lock at LockFrom and unlock at LockUntil (local "HH:MM"; the
	// window runs overnight when LockFrom > LockUntil). LockDays 0 is off.
	LockDays  int64
	LockFrom  string
	LockUntil string
	// LockIdleHours locks the displays after that long untouched; 0 is off.
	LockIdleHours int64
	// UnlockCodeHash is the household unlock code's argon2id hash, or "".
	UnlockCodeHash string
	// UnlockCodeLength is how many digits the code has, or 0 if not known
	// (a code set before lengths were stored, until it next unlocks).
	UnlockCodeLength int

	dayStart, lockFrom, lockUntil time.Duration
}

// HasUnlockCode reports whether a household unlock code is set.
func (s Settings) HasUnlockCode() bool { return s.UnlockCodeHash != "" }

// LockIdle is how long the displays may go untouched before locking; zero
// means never.
func (s Settings) LockIdle() time.Duration { return time.Duration(s.LockIdleHours) * time.Hour }

// LockWindow returns the lock-schedule window containing now, or the most
// recent one that started before it, and whether now is inside it. ok is
// false when the schedule is off. Windows start on a LockDays weekday at
// LockFrom (local) and end at the next LockUntil.
func (s Settings) LockWindow(now time.Time) (start, end time.Time, in, ok bool) {
	if s.LockDays == 0 || s.lockFrom == s.lockUntil {
		return time.Time{}, time.Time{}, false, false
	}
	local := now.In(s.Location)
	y, m, d := local.Date()
	// The latest start at or before now is at most 7 days back (8 covers an
	// overnight window that began the day before a full week ago).
	for back := 0; back <= 7; back++ {
		day := time.Date(y, m, d-back, 0, 0, 0, 0, s.Location)
		if s.LockDays&(1<<int(day.Weekday())) == 0 {
			continue
		}
		start = atClock(day, s.lockFrom, s.Location)
		if start.After(now) {
			continue
		}
		endDay := day
		if s.lockUntil <= s.lockFrom {
			endDay = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, s.Location)
		}
		end = atClock(endDay, s.lockUntil, s.Location)
		return start, end, now.Before(end), true
	}
	return time.Time{}, time.Time{}, false, true
}

// atClock is the wall-clock time off (from midnight) on day, built from
// hours and minutes so days with a clock change land on the right time.
func atClock(day time.Time, off time.Duration, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), int(off/time.Hour), int(off%time.Hour/time.Minute), 0, 0, loc)
}

// AdminIdle is how long an admin unlock on a household display lasts
// without activity.
func (s Settings) AdminIdle() time.Duration {
	return time.Duration(s.AdminIdleMinutes) * time.Minute
}

// ScreensaverIdle is how long a household display waits, untouched, before
// showing the screensaver. Zero means never.
func (s Settings) ScreensaverIdle() time.Duration {
	return time.Duration(s.ScreensaverIdleMinutes) * time.Minute
}

// CalendarIdle is how long a household display shows the board calendar,
// untouched, before going back to the chores. Zero means never.
func (s Settings) CalendarIdle() time.Duration {
	return time.Duration(s.CalendarIdleMinutes) * time.Minute
}

// BoardDay returns midnight (local) of the board's current day. Before
// DayStartsAt the board still shows the previous day, so e.g. with a 04:00
// reset 01:30 on Tuesday is still Monday.
func (s Settings) BoardDay(now time.Time) time.Time {
	// Compare wall-clock time of day rather than subtracting a duration, so
	// days when the clocks change (23 or 25 hours long) roll over at the
	// right local time.
	local := now.In(s.Location)
	y, m, d := local.Date()
	sinceMidnight := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute +
		time.Duration(local.Second())*time.Second
	if sinceMidnight < s.dayStart {
		d--
	}
	return time.Date(y, m, d, 0, 0, 0, 0, s.Location)
}

// Today returns the board's current day as YYYY-MM-DD.
func (s Settings) Today(now time.Time) string {
	return s.BoardDay(now).Format(DateLayout)
}

// Input is an update from the settings form.
type Input struct {
	HouseholdName          string
	Timezone               string
	DayStartsAt            string
	AdminIdleMinutes       int64
	ScreensaverIdleMinutes int64
	CalendarIdleMinutes    int64
	CalendarChores         bool
	LockDays               int64
	LockFrom               string
	LockUntil              string
	LockIdleHours          int64
}

// Service loads, caches and updates settings.
type Service struct {
	st  *store.Store
	now func() time.Time

	mu    sync.RWMutex
	cache map[int64]Settings
}

// New returns a settings Service.
func New(st *store.Store) *Service {
	return &Service{st: st, now: time.Now, cache: map[int64]Settings{}}
}

// Ensure creates the settings row for householdID with defaultTZ if it
// doesn't exist yet. Once created, the stored timezone wins over defaultTZ.
func (s *Service) Ensure(ctx context.Context, householdID int64, defaultTZ *time.Location) error {
	if err := s.st.Q.InsertDefaultSettings(ctx, dbq.InsertDefaultSettingsParams{
		HouseholdID: householdID,
		Timezone:    defaultTZ.String(),
		UpdatedAt:   s.now().Unix(),
	}); err != nil {
		return fmt.Errorf("insert default settings: %w", err)
	}
	s.Invalidate(householdID)
	return nil
}

// Get returns the cached settings for householdID, loading them on a miss.
func (s *Service) Get(ctx context.Context, householdID int64) (Settings, error) {
	s.mu.RLock()
	cached, ok := s.cache[householdID]
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}

	row, err := s.st.Q.GetSettings(ctx, householdID)
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	h, err := s.st.Q.GetHousehold(ctx, householdID)
	if err != nil {
		return Settings{}, fmt.Errorf("get household: %w", err)
	}
	out, err := resolve(h.Name, row)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.cache[householdID] = out
	s.mu.Unlock()
	return out, nil
}

// Location returns the household timezone for callers with no way to report
// an error; it falls back to UTC if settings can't be loaded.
func (s *Service) Location(ctx context.Context, householdID int64) *time.Location {
	set, err := s.Get(ctx, householdID)
	if err != nil {
		return time.UTC
	}
	return set.Location
}

// DayStart is when the household's board day rolls over, as time since
// local midnight (0 if settings can't be loaded).
func (s *Service) DayStart(ctx context.Context, householdID int64) time.Duration {
	set, err := s.Get(ctx, householdID)
	if err != nil {
		return 0
	}
	return set.dayStart
}

// Update validates and saves in, logging a settings.updated activity row as
// actorID. Validation problems are returned as valid.Errors.
func (s *Service) Update(ctx context.Context, householdID, actorID int64, in Input) error {
	errs := valid.Errors{}
	name := strings.TrimSpace(in.HouseholdName)
	if name == "" || len([]rune(name)) > 50 {
		errs.Add("household_name", "Enter a household name (up to 50 characters).")
	}
	if _, err := LoadTimezone(in.Timezone); err != nil {
		errs.Add("timezone", "Choose a timezone.")
	}
	if _, err := ParseClock(in.DayStartsAt); err != nil {
		errs.Add("day_starts_at", "Enter a time like 04:00.")
	}
	if in.AdminIdleMinutes < 1 || in.AdminIdleMinutes > 60 {
		errs.Add("admin_idle_minutes", "Choose between 1 and 60 minutes.")
	}
	if in.ScreensaverIdleMinutes < 0 || in.ScreensaverIdleMinutes > 120 {
		errs.Add("screensaver_idle_minutes", "Choose between 0 (never) and 120 minutes.")
	}
	if in.CalendarIdleMinutes < 0 || in.CalendarIdleMinutes > 60 {
		errs.Add("calendar_idle_minutes", "Choose between 0 (never) and 60 minutes.")
	}
	if in.LockDays < 0 || in.LockDays > 127 {
		errs.Add("lock_days", "Choose the days to lock.")
	}
	from, ferr := ParseClock(in.LockFrom)
	if ferr != nil {
		errs.Add("lock_from", "Enter a time like 09:00.")
	}
	until, uerr := ParseClock(in.LockUntil)
	if uerr != nil {
		errs.Add("lock_until", "Enter a time like 15:00.")
	}
	if ferr == nil && uerr == nil && from == until && in.LockDays != 0 {
		errs.Add("lock_until", "Choose a different time to unlock.")
	}
	if in.LockIdleHours < 0 || in.LockIdleHours > 72 {
		errs.Add("lock_idle_hours", "Choose between 0 (never) and 72 hours.")
	}
	if err := errs.Err(); err != nil {
		return err
	}

	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := q.UpdateHouseholdName(ctx, dbq.UpdateHouseholdNameParams{Name: name, ID: householdID}); err != nil {
			return fmt.Errorf("update household name: %w", err)
		}
		if _, err := q.UpdateSettings(ctx, dbq.UpdateSettingsParams{
			Timezone:               in.Timezone,
			DayStartsAt:            in.DayStartsAt,
			AdminIdleMinutes:       in.AdminIdleMinutes,
			ScreensaverIdleMinutes: in.ScreensaverIdleMinutes,
			CalendarIdleMinutes:    in.CalendarIdleMinutes,
			CalendarChores:         boolInt(in.CalendarChores),
			LockDays:               in.LockDays,
			LockFrom:               in.LockFrom,
			LockUntil:              in.LockUntil,
			LockIdleHours:          in.LockIdleHours,
			UpdatedAt:              now.Unix(),
			HouseholdID:            householdID,
		}); err != nil {
			return fmt.Errorf("update settings: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "settings.updated",
			SubjectType: "household", SubjectID: householdID, At: now,
		})
	})
	s.Invalidate(householdID)
	return err
}

// Invalidate drops cached settings for householdID (e.g. after first-run
// setup renames the household).
func (s *Service) Invalidate(householdID int64) {
	s.mu.Lock()
	delete(s.cache, householdID)
	s.mu.Unlock()
}

func resolve(name string, row dbq.HouseholdSetting) (Settings, error) {
	loc, err := LoadTimezone(row.Timezone)
	if err != nil {
		return Settings{}, fmt.Errorf("stored timezone %q: %w", row.Timezone, err)
	}
	dayStart, err := ParseClock(row.DayStartsAt)
	if err != nil {
		return Settings{}, fmt.Errorf("stored day_starts_at %q: %w", row.DayStartsAt, err)
	}
	lockFrom, err := ParseClock(row.LockFrom)
	if err != nil {
		return Settings{}, fmt.Errorf("stored lock_from %q: %w", row.LockFrom, err)
	}
	lockUntil, err := ParseClock(row.LockUntil)
	if err != nil {
		return Settings{}, fmt.Errorf("stored lock_until %q: %w", row.LockUntil, err)
	}
	code, codeLength := "", 0
	if row.UnlockCodeHash != nil {
		code = *row.UnlockCodeHash
		if row.UnlockCodeLength != nil {
			codeLength = int(*row.UnlockCodeLength)
		}
	}
	return Settings{
		HouseholdID:            row.HouseholdID,
		HouseholdName:          name,
		Location:               loc,
		DayStartsAt:            row.DayStartsAt,
		AdminIdleMinutes:       row.AdminIdleMinutes,
		ScreensaverIdleMinutes: row.ScreensaverIdleMinutes,
		CalendarIdleMinutes:    row.CalendarIdleMinutes,
		CalendarChores:         row.CalendarChores == 1,
		LockDays:               row.LockDays,
		LockFrom:               row.LockFrom,
		LockUntil:              row.LockUntil,
		LockIdleHours:          row.LockIdleHours,
		UnlockCodeHash:         code,
		UnlockCodeLength:       codeLength,
		dayStart:               dayStart,
		lockFrom:               lockFrom,
		lockUntil:              lockUntil,
	}, nil
}

// LoadTimezone loads an IANA zone, refusing "" and "Local" (which would
// silently mean the server's zone).
func LoadTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("timezone %q: not an IANA zone name", name)
	}
	return time.LoadLocation(name)
}

// ParseClock parses "HH:MM" (24-hour) into an offset from midnight.
func ParseClock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, fmt.Errorf("time %q: want HH:MM", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
