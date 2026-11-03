// Package settings stores per-household preferences (name, timezone, when
// the board day rolls over, admin idle timeout) and caches them so hot paths
// such as session checks and board rendering don't hit the database.
package settings

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
	"github.com/lukeskelhorn/domus/internal/valid"
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

	dayStart time.Duration
}

// AdminIdle is how long an admin unlock on a household display lasts
// without activity.
func (s Settings) AdminIdle() time.Duration {
	return time.Duration(s.AdminIdleMinutes) * time.Minute
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
	HouseholdName    string
	Timezone         string
	DayStartsAt      string
	AdminIdleMinutes int64
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
	if err := errs.Err(); err != nil {
		return err
	}

	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := q.UpdateHouseholdName(ctx, dbq.UpdateHouseholdNameParams{Name: name, ID: householdID}); err != nil {
			return fmt.Errorf("update household name: %w", err)
		}
		if _, err := q.UpdateSettings(ctx, dbq.UpdateSettingsParams{
			Timezone:         in.Timezone,
			DayStartsAt:      in.DayStartsAt,
			AdminIdleMinutes: in.AdminIdleMinutes,
			UpdatedAt:        now.Unix(),
			HouseholdID:      householdID,
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
	return Settings{
		HouseholdID:      row.HouseholdID,
		HouseholdName:    name,
		Location:         loc,
		DayStartsAt:      row.DayStartsAt,
		AdminIdleMinutes: row.AdminIdleMinutes,
		dayStart:         dayStart,
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
