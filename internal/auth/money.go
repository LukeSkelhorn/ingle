package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// MoneyTokenTTL is how long a My money PIN check lasts without being used.
const MoneyTokenTTL = 60 * time.Second

// MoneyTokenMax is the longest a My money PIN check lasts, however much it's used.
const MoneyTokenMax = 10 * time.Minute

type moneyGrant struct {
	householdID int64
	personID    int64
	deviceID    int64
	issued      time.Time
	expires     time.Time
}

// Deliberately not sessions, so a money token can never pass as an adult's session.
type moneyTokens struct {
	mu     sync.Mutex
	grants map[string]moneyGrant
}

// CheckMoneyPIN checks the PIN of personID, a child or an adult, on a registered display.
func (s *Service) CheckMoneyPIN(ctx context.Context, device *dbq.Device, personID int64, pin, clientAddr string) (string, error) {
	if device == nil {
		return "", ErrNotDisplay
	}
	pKey, addrKey := pinKey(personID), "addr:"+clientAddr
	for _, b := range []struct {
		l   *Limiter
		key string
	}{{s.pinLimiter, pKey}, {s.addrLimiter, addrKey}} {
		if blocked, _ := b.l.Blocked(b.key); blocked {
			return "", ErrLockedOut
		}
	}
	p, err := s.st.Q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: device.HouseholdID, ID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("get person: %w", err)
	}
	ok := err == nil && p.ArchivedAt == nil && p.PinHash != nil
	if ok {
		ok = VerifyPIN(*p.PinHash, pin)
	} else {
		VerifyPIN(dummyHash, pin)
	}
	if !ok {
		pinLocked := s.pinLimiter.Fail(pKey)
		addrLocked := s.addrLimiter.Fail(addrKey)
		if pinLocked || addrLocked {
			return "", ErrLockedOut
		}
		return "", ErrBadCredentials
	}
	s.pinLimiter.Reset(pKey)
	if p.PinLength == nil {
		s.learnPINLength(ctx, device.HouseholdID, personID, pin)
	}
	token := NewToken()
	now := s.now()
	s.money.mu.Lock()
	defer s.money.mu.Unlock()
	for k, g := range s.money.grants {
		if !now.Before(g.expires) {
			delete(s.money.grants, k)
		}
	}
	s.money.grants[string(HashToken(token))] = moneyGrant{
		householdID: device.HouseholdID, personID: personID, deviceID: device.ID, issued: now, expires: now.Add(MoneyTokenTTL),
	}
	return token, nil
}

// MoneyToken reports whose My money token is, if it's still good on device.
func (s *Service) MoneyToken(device *dbq.Device, token string) (int64, bool) {
	if device == nil || token == "" {
		return 0, false
	}
	key := string(HashToken(token))
	s.money.mu.Lock()
	defer s.money.mu.Unlock()
	g, ok := s.money.grants[key]
	if !ok {
		return 0, false
	}
	if !s.now().Before(g.expires) {
		delete(s.money.grants, key)
		return 0, false
	}
	if g.deviceID != device.ID || g.householdID != device.HouseholdID {
		return 0, false
	}
	g.expires = s.now().Add(MoneyTokenTTL)
	if limit := g.issued.Add(MoneyTokenMax); g.expires.After(limit) {
		g.expires = limit
	}
	s.money.grants[key] = g
	return g.personID, true
}
