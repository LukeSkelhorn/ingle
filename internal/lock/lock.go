// Package lock locks the household displays when the house is empty, so names, chores and calendar events aren't on show.
package lock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Why the displays were locked.
const (
	Manual   = "manual"
	Remote   = "remote"
	Schedule = "schedule"
	Idle     = "idle"
)

// How the displays were unlocked.
const (
	ByCode     = "code"
	ByPIN      = "pin"
	ByPhone    = "phone"
	BySchedule = "schedule"
)

// State is whether a household's displays are locked.
type State struct {
	Locked bool
	Reason string
	By     *int64
	Since  time.Time
}

// EndSessions ends adult (PIN) sessions on every display.
type EndSessions func(ctx context.Context, householdID int64) error

// Service reads and changes the lock state.
type Service struct {
	st          *store.Store
	set         *settings.Service
	endSessions EndSessions
	now         func() time.Time
	onChange    func(householdID int64)

	mu         sync.Mutex
	cache      map[int64]State
	lastActive map[int64]time.Time
	started    time.Time
}

// New returns a lock Service.
func New(st *store.Store, set *settings.Service, end EndSessions) *Service {
	return &Service{
		st: st, set: set, endSessions: end, now: time.Now, onChange: func(int64) {},
		cache: map[int64]State{}, lastActive: map[int64]time.Time{}, started: time.Now(),
	}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	s.started = now()
}

// OnChange registers fn to be called after every lock and unlock.
func (s *Service) OnChange(fn func(householdID int64)) { s.onChange = fn }

// State returns the household's lock state.
func (s *Service) State(ctx context.Context, householdID int64) (State, error) {
	s.mu.Lock()
	st, ok := s.cache[householdID]
	s.mu.Unlock()
	if ok {
		return st, nil
	}
	row, err := s.row(ctx, s.st.Q, householdID)
	if err != nil {
		return State{}, err
	}
	st = State{Locked: row.Locked == 1, Reason: row.Reason, By: row.LockedBy, Since: time.Unix(row.ChangedAt, 0)}
	s.mu.Lock()
	s.cache[householdID] = st
	s.mu.Unlock()
	return st, nil
}

func (s *Service) row(ctx context.Context, q *dbq.Queries, householdID int64) (dbq.HouseholdLock, error) {
	row, err := q.GetLock(ctx, householdID)
	if errors.Is(err, sql.ErrNoRows) {
		return dbq.HouseholdLock{HouseholdID: householdID}, nil
	}
	if err != nil {
		return dbq.HouseholdLock{}, fmt.Errorf("get lock: %w", err)
	}
	return row, nil
}

// Touch records activity on a display, which holds off the idle lock.
func (s *Service) Touch(householdID int64) {
	s.mu.Lock()
	s.lastActive[householdID] = s.now()
	s.mu.Unlock()
}

func (s *Service) idleSince(householdID int64) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.lastActive[householdID]; ok {
		return t
	}
	return s.started
}

// Lock locks the displays.
func (s *Service) Lock(ctx context.Context, householdID int64, by *int64, reason string) error {
	return s.change(ctx, householdID, func(row *dbq.HouseholdLock) (string, bool) {
		if row.Locked == 1 {
			return "", false
		}
		row.Locked, row.Reason, row.LockedBy = 1, reason, by
		switch reason {
		case Schedule:
			return "board.locked_schedule", true
		case Idle:
			return "board.locked_idle", true
		}
		return "board.locked", true
	}, by)
}

// Unlock unlocks the displays.
func (s *Service) Unlock(ctx context.Context, householdID int64, by *int64, how string) error {
	err := s.change(ctx, householdID, func(row *dbq.HouseholdLock) (string, bool) {
		if row.Locked == 0 {
			return "", false
		}
		row.Locked, row.Reason, row.LockedBy = 0, "", nil
		switch how {
		case ByCode:
			return "board.unlocked_code", true
		case BySchedule:
			return "board.unlocked_schedule", true
		}
		return "board.unlocked", true
	}, by)
	if err == nil {
		s.Touch(householdID)
	}
	return err
}

func (s *Service) change(ctx context.Context, householdID int64, edit func(*dbq.HouseholdLock) (verb string, changed bool), actor *int64) error {
	now := s.now()
	changed := false
	var locked bool
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		row, err := s.row(ctx, q, householdID)
		if err != nil {
			return err
		}
		verb, ok := edit(&row)
		if !ok {
			return nil
		}
		changed, locked = true, row.Locked == 1
		row.ChangedAt = now.Unix()
		if err := s.write(ctx, q, row); err != nil {
			return err
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: actor, Verb: verb,
			SubjectType: "household", SubjectID: householdID, At: now,
		})
	})
	if err != nil || !changed {
		return err
	}
	s.Invalidate(householdID)
	if locked && s.endSessions != nil {
		if err := s.endSessions(ctx, householdID); err != nil {
			return fmt.Errorf("end display sessions: %w", err)
		}
	}
	s.onChange(householdID)
	return nil
}

func (s *Service) write(ctx context.Context, q *dbq.Queries, row dbq.HouseholdLock) error {
	if err := q.SetLock(ctx, dbq.SetLockParams(row)); err != nil {
		return fmt.Errorf("set lock: %w", err)
	}
	return nil
}

// Invalidate drops the cached lock state, e.g. after the rows were deleted.
func (s *Service) Invalidate(householdID int64) {
	s.mu.Lock()
	delete(s.cache, householdID)
	s.mu.Unlock()
}

// Tick applies the schedule and the idle lock; call it every minute.
func (s *Service) Tick(ctx context.Context, householdID int64) error {
	set, err := s.set.Get(ctx, householdID)
	if err != nil {
		return err
	}
	row, err := s.row(ctx, s.st.Q, householdID)
	if err != nil {
		return err
	}
	now := s.now()
	start, _, in, on := set.LockWindow(now)
	switch {
	case on && in && start.Unix() != row.ScheduleApplied:
		if err := s.markApplied(ctx, householdID, start); err != nil {
			return err
		}
		if err := s.Lock(ctx, householdID, nil, Schedule); err != nil {
			return err
		}
	case (!on || !in) && row.Locked == 1 && row.Reason == Schedule:
		if err := s.Unlock(ctx, householdID, nil, BySchedule); err != nil {
			return err
		}
	}
	if idle := set.LockIdle(); idle > 0 && now.Sub(s.idleSince(householdID)) >= idle {
		return s.Lock(ctx, householdID, nil, Idle)
	}
	return nil
}

func (s *Service) markApplied(ctx context.Context, householdID int64, start time.Time) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		row, err := s.row(ctx, q, householdID)
		if err != nil {
			return err
		}
		row.ScheduleApplied = start.Unix()
		return s.write(ctx, q, row)
	})
}
