package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// ErrNoUnlockCode is returned when clearing a code that isn't set.
var ErrNoUnlockCode = errors.New("no household unlock code is set")

// CheckUnlockCode checks code, entered on a locked household display,
// against the household unlock code and then every adult's PIN. It returns
// the adult whose PIN matched, or nil for the household code. Wrong codes
// are limited per display and, more loosely, per client address.
func (s *Service) CheckUnlockCode(ctx context.Context, device *dbq.Device, code, clientAddr string) (*int64, error) {
	if device == nil {
		return nil, ErrNotDisplay
	}
	devKey, addrKey := "unlock:"+strconv.FormatInt(device.ID, 10), "addr:"+clientAddr
	if blocked, _ := s.codeLimiter.Blocked(devKey); blocked {
		return nil, ErrLockedOut
	}
	if blocked, _ := s.addrLimiter.Blocked(addrKey); blocked {
		return nil, ErrLockedOut
	}
	set, err := s.settings.Get(ctx, device.HouseholdID)
	if err != nil {
		return nil, err
	}
	pins, err := s.st.Q.ListUnlockPINs(ctx, device.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("list PINs: %w", err)
	}

	// Always do at least one hash so a wrong guess takes as long whether
	// or not a household code is set.
	var who *int64
	ok := false
	if set.HasUnlockCode() {
		ok = VerifyPIN(set.UnlockCodeHash, code)
	} else {
		VerifyPIN(dummyHash, code)
	}
	for i := 0; !ok && i < len(pins); i++ {
		if pins[i].PinHash != nil && VerifyPIN(*pins[i].PinHash, code) {
			ok, who = true, &pins[i].ID
		}
	}
	if !ok {
		devLocked := s.codeLimiter.Fail(devKey)
		addrLocked := s.addrLimiter.Fail(addrKey)
		if devLocked || addrLocked {
			return nil, ErrLockedOut
		}
		return nil, ErrBadCredentials
	}
	s.codeLimiter.Reset(devKey)
	return who, nil
}

// SetUnlockCode sets (or replaces) the household unlock code.
func (s *Service) SetUnlockCode(ctx context.Context, householdID, actorID int64, code string) error {
	hash, err := HashPIN(code)
	if err != nil {
		return err
	}
	return s.setUnlockCode(ctx, householdID, actorID, &hash, "lock.code_set")
}

// ClearUnlockCode removes the household unlock code; adult PINs still
// unlock the displays.
func (s *Service) ClearUnlockCode(ctx context.Context, householdID, actorID int64) error {
	return s.setUnlockCode(ctx, householdID, actorID, nil, "lock.code_cleared")
}

func (s *Service) setUnlockCode(ctx context.Context, householdID, actorID int64, hash *string, verb string) error {
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := q.SetUnlockCode(ctx, dbq.SetUnlockCodeParams{UnlockCodeHash: hash, UpdatedAt: now.Unix(), HouseholdID: householdID}); err != nil {
			return fmt.Errorf("set unlock code: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: verb,
			SubjectType: "household", SubjectID: householdID, At: now,
		})
	})
	s.settings.Invalidate(householdID)
	return err
}

// CanUnlockOnDisplay reports whether anything can unlock a locked display
// in place: a household code or at least one adult PIN.
func (s *Service) CanUnlockOnDisplay(ctx context.Context, householdID int64) (bool, error) {
	set, err := s.settings.Get(ctx, householdID)
	if err != nil {
		return false, err
	}
	if set.HasUnlockCode() {
		return true, nil
	}
	pins, err := s.st.Q.ListUnlockPINs(ctx, householdID)
	if err != nil {
		return false, fmt.Errorf("list PINs: %w", err)
	}
	return len(pins) > 0, nil
}

// EndDisplaySessions locks admin on every household display.
func (s *Service) EndDisplaySessions(ctx context.Context, householdID int64) error {
	return s.st.Q.DeleteDisplaySessions(ctx, householdID)
}
