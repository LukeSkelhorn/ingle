package module

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// State is which modules a household has on.
type State struct {
	On map[string]bool
	// Chosen is false until an adult has saved Admin › Modules (or set the
	// household up). Until then every module is on, so an upgrade changes
	// nothing.
	Chosen bool
}

// Service reads and changes each household's modules. Every request checks
// them, so the state is cached, the same way as the lock state.
type Service struct {
	st       *store.Store
	reg      *Registry
	now      func() time.Time
	onChange func(householdID int64)

	mu    sync.Mutex
	cache map[int64]State
}

// NewService returns a Service for the modules in reg.
func NewService(st *store.Store, reg *Registry) *Service {
	return &Service{st: st, reg: reg, now: time.Now, onChange: func(int64) {}, cache: map[int64]State{}}
}

// Registry returns the modules the Service knows.
func (s *Service) Registry() *Registry { return s.reg }

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// OnChange registers fn to be called after the modules change (the server
// uses it to tell open pages).
func (s *Service) OnChange(fn func(householdID int64)) { s.onChange = fn }

// State returns the household's modules.
func (s *Service) State(ctx context.Context, householdID int64) (State, error) {
	s.mu.Lock()
	st, ok := s.cache[householdID]
	s.mu.Unlock()
	if ok {
		return st, nil
	}
	rows, err := s.st.Q.ListModules(ctx, householdID)
	if err != nil {
		return State{}, fmt.Errorf("list modules: %w", err)
	}
	st = State{On: map[string]bool{}, Chosen: len(rows) > 0}
	saved := map[string]bool{}
	for _, row := range rows {
		saved[row.ModuleID] = row.Enabled == 1
	}
	for _, m := range s.reg.mods {
		v, ok := saved[m.ID()]
		switch {
		case !st.Chosen:
			v = true
		case !ok:
			// A module added in a later version: its default.
			v = m.DefaultOn()
		}
		st.On[m.ID()] = v
	}
	// Keep requirements consistent even if the rows aren't.
	st.On = s.reg.Plan(st.On, st.On).Final
	s.mu.Lock()
	s.cache[householdID] = st
	s.mu.Unlock()
	return st, nil
}

// Enabled reports whether the module id is on. If the state can't be read
// it says on, which is how Ingle behaved before modules.
func (s *Service) Enabled(ctx context.Context, householdID int64, id string) bool {
	if _, ok := s.reg.byID[id]; !ok {
		return false
	}
	st, err := s.State(ctx, householdID)
	if err != nil {
		return true
	}
	return st.On[id]
}

// Set saves every module's state from final (usually a Plan's Final) and
// marks the household as having chosen. actor is the adult making the
// change.
func (s *Service) Set(ctx context.Context, householdID int64, actor *int64, final map[string]bool) error {
	before, err := s.State(ctx, householdID)
	if err != nil {
		return err
	}
	now := s.now()
	err = s.st.WithTx(ctx, func(q *dbq.Queries) error {
		for _, m := range s.reg.mods {
			on := int64(0)
			if final[m.ID()] {
				on = 1
			}
			if err := q.SetModule(ctx, dbq.SetModuleParams{
				HouseholdID: householdID, ModuleID: m.ID(), Enabled: on, ChangedAt: now.Unix(),
			}); err != nil {
				return fmt.Errorf("set module %s: %w", m.ID(), err)
			}
		}
		if before.Chosen && maps.Equal(before.On, final) {
			return nil
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: actor, Verb: "modules.updated",
			SubjectType: "household", SubjectID: householdID, At: now,
		})
	})
	if err != nil {
		return err
	}
	s.Invalidate(householdID)
	s.onChange(householdID)
	return nil
}

// Invalidate drops the cached state, e.g. after the rows were changed
// directly.
func (s *Service) Invalidate(householdID int64) {
	s.mu.Lock()
	delete(s.cache, householdID)
	s.mu.Unlock()
}

// SetDefaults gives a new household each module's default.
func (s *Service) SetDefaults(ctx context.Context, householdID int64, actor *int64) error {
	return s.Set(ctx, householdID, actor, s.reg.Plan(nil, s.reg.Defaults()).Final)
}
