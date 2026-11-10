package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Session kinds.
const (
	KindDisplay  = "display"
	KindPersonal = "personal"
)

// PersonalSessionTTL is how long a personal-device sign-in lasts without use.
const PersonalSessionTTL = 30 * 24 * time.Hour

// Failure limits for PIN and password attempts. The per-address limit is
// looser because everyone behind a reverse proxy shares one address.
const (
	MaxFailures        = 5
	MaxAddressFailures = 20
	LockoutPeriod      = 5 * time.Minute
)

// touchInterval throttles last_seen writes for displays and personal
// sessions; display sessions are always touched so the idle timeout is exact.
const touchInterval = time.Minute

// Errors returned by Service.
var (
	ErrBadCredentials = errors.New("incorrect details")
	ErrLockedOut      = errors.New("too many attempts, try again later")
	ErrNotDisplay     = errors.New("PIN unlock only works on a household display")
)

// Session is a validated, signed-in adult.
type Session struct {
	Kind     string
	Person   dbq.Person
	DeviceID *int64
	// TokenHash identifies the session for sign-out and revocation.
	TokenHash []byte
}

// Service manages household displays and adult sessions.
type Service struct {
	st       *store.Store
	settings *settings.Service
	now      func() time.Time

	pinLimiter  *Limiter
	codeLimiter *Limiter
	pwLimiter   *Limiter
	addrLimiter *Limiter

	money moneyTokens
}

// New returns an auth Service.
func New(st *store.Store, set *settings.Service) *Service {
	return &Service{
		st:          st,
		settings:    set,
		now:         time.Now,
		pinLimiter:  NewLimiter(MaxFailures, LockoutPeriod),
		codeLimiter: NewLimiter(MaxFailures, LockoutPeriod),
		pwLimiter:   NewLimiter(MaxFailures, LockoutPeriod),
		addrLimiter: NewLimiter(MaxAddressFailures, LockoutPeriod),
		money:       moneyTokens{grants: map[string]moneyGrant{}},
	}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.now = now
	s.pinLimiter.now = now
	s.codeLimiter.now = now
	s.pwLimiter.now = now
	s.addrLimiter.now = now
}

// RegisterDisplay registers the current device as a household display and
// returns the token to store in its cookie.
func (s *Service) RegisterDisplay(ctx context.Context, householdID int64, name string, by int64) (string, dbq.Device, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 50 {
		return "", dbq.Device{}, errors.New("display name must be 1 to 50 characters")
	}
	token := NewToken()
	now := s.now()
	var dev dbq.Device
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		dev, err = q.CreateDevice(ctx, dbq.CreateDeviceParams{
			HouseholdID: householdID,
			Name:        name,
			TokenHash:   HashToken(token),
			CreatedBy:   &by,
			CreatedAt:   now.Unix(),
			LastSeenAt:  now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create device: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &by, Verb: "device.registered",
			SubjectType: "device", SubjectID: dev.ID, At: now,
		})
	})
	if err != nil {
		return "", dbq.Device{}, err
	}
	return token, dev, nil
}

// Display returns the registered, unrevoked display for token, or nil.
func (s *Service) Display(ctx context.Context, token string) (*dbq.Device, error) {
	if token == "" {
		return nil, nil
	}
	dev, err := s.st.Q.GetDeviceByToken(ctx, HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device: %w", err)
	}
	if now := s.now(); now.Sub(time.Unix(dev.LastSeenAt, 0)) >= touchInterval {
		if err := s.st.Q.TouchDevice(ctx, dbq.TouchDeviceParams{LastSeenAt: now.Unix(), ID: dev.ID}); err != nil {
			return nil, fmt.Errorf("touch device: %w", err)
		}
	}
	return &dev, nil
}

// PINLocked reports whether personID is temporarily blocked from PIN entry.
func (s *Service) PINLocked(personID int64) bool {
	blocked, _ := s.pinLimiter.Blocked(pinKey(personID))
	return blocked
}

// UnlockWithPIN checks personID's PIN on a registered display and returns a
// display session token. After MaxFailures wrong PINs the person is locked
// out for LockoutPeriod.
func (s *Service) UnlockWithPIN(ctx context.Context, device *dbq.Device, personID int64, pin string) (string, error) {
	if device == nil {
		return "", ErrNotDisplay
	}
	key := pinKey(personID)
	if blocked, _ := s.pinLimiter.Blocked(key); blocked {
		return "", ErrLockedOut
	}
	p, err := s.st.Q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: device.HouseholdID, ID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("get person: %w", err)
	}
	ok := err == nil && p.Kind == "adult" && p.ArchivedAt == nil && p.PinHash != nil
	if ok {
		ok = VerifyPIN(*p.PinHash, pin)
	} else {
		VerifyPIN(dummyHash, pin)
	}
	if !ok {
		if s.pinLimiter.Fail(key) {
			return "", ErrLockedOut
		}
		return "", ErrBadCredentials
	}
	s.pinLimiter.Reset(key)
	if p.PinLength == nil {
		s.learnPINLength(ctx, device.HouseholdID, personID, pin)
	}
	return s.createSession(ctx, p, KindDisplay, &device.ID, "")
}

// SignIn checks an adult's email and password and returns a personal session
// token. Attempts are limited per email and, more loosely, per client
// address.
func (s *Service) SignIn(ctx context.Context, email, password, userAgent, clientAddr string) (string, dbq.Person, error) {
	email = strings.TrimSpace(email)
	emailKey, addrKey := "email:"+strings.ToLower(email), "addr:"+clientAddr
	if blocked, _ := s.pwLimiter.Blocked(emailKey); blocked {
		return "", dbq.Person{}, ErrLockedOut
	}
	if blocked, _ := s.addrLimiter.Blocked(addrKey); blocked {
		return "", dbq.Person{}, ErrLockedOut
	}
	p, err := s.st.Q.GetPersonByEmail(ctx, &email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", dbq.Person{}, fmt.Errorf("get person: %w", err)
	}
	ok := err == nil && p.Kind == "adult" && p.PasswordHash != nil
	if ok {
		ok = VerifyPassword(*p.PasswordHash, password)
	} else {
		VerifyPassword(dummyHash, password)
	}
	if !ok {
		emailLocked := s.pwLimiter.Fail(emailKey)
		addrLocked := s.addrLimiter.Fail(addrKey)
		if emailLocked || addrLocked {
			return "", dbq.Person{}, ErrLockedOut
		}
		return "", dbq.Person{}, ErrBadCredentials
	}
	s.pwLimiter.Reset(emailKey)
	token, err := s.createSession(ctx, p, KindPersonal, nil, userAgent)
	return token, p, err
}

// StartPersonalSession signs personID in without a password, for the end of
// first-run setup where they have just chosen one.
func (s *Service) StartPersonalSession(ctx context.Context, p dbq.Person, userAgent string) (string, error) {
	return s.createSession(ctx, p, KindPersonal, nil, userAgent)
}

func (s *Service) createSession(ctx context.Context, p dbq.Person, kind string, deviceID *int64, userAgent string) (string, error) {
	if p.Kind != "adult" || p.ArchivedAt != nil {
		return "", ErrBadCredentials
	}
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	token := NewToken()
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := q.CreateSession(ctx, dbq.CreateSessionParams{
			TokenHash:   HashToken(token),
			HouseholdID: p.HouseholdID,
			PersonID:    p.ID,
			Kind:        kind,
			DeviceID:    deviceID,
			UserAgent:   userAgent,
			CreatedAt:   now.Unix(),
			LastSeenAt:  now.Unix(),
		}); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		verb := "admin.signed_in"
		if kind == KindDisplay {
			verb = "admin.unlocked"
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: p.HouseholdID, ActorID: &p.ID, Verb: verb,
			SubjectType: "person", SubjectID: p.ID, At: now,
		})
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Session validates token and returns the signed-in adult, or nil if the
// token is unknown, expired, belongs to an archived person, or is a display
// session presented by a different device. Valid sessions are touched so the
// expiry slides.
func (s *Service) Session(ctx context.Context, token string, device *dbq.Device) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	hash := HashToken(token)
	row, err := s.st.Q.GetSessionByToken(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess, p := row.Session, row.Person
	now := s.now()
	lastSeen := time.Unix(sess.LastSeenAt, 0)

	var ttl time.Duration
	switch sess.Kind {
	case KindDisplay:
		if device == nil || sess.DeviceID == nil || *sess.DeviceID != device.ID {
			return nil, nil
		}
		set, err := s.settings.Get(ctx, sess.HouseholdID)
		if err != nil {
			return nil, err
		}
		ttl = set.AdminIdle()
	default:
		ttl = PersonalSessionTTL
	}

	if p.ArchivedAt != nil || p.Kind != "adult" || now.Sub(lastSeen) >= ttl {
		if err := s.st.Q.DeleteSession(ctx, hash); err != nil {
			return nil, fmt.Errorf("delete session: %w", err)
		}
		return nil, nil
	}
	if sess.Kind == KindDisplay || now.Sub(lastSeen) >= touchInterval {
		if err := s.st.Q.TouchSession(ctx, dbq.TouchSessionParams{LastSeenAt: now.Unix(), TokenHash: hash}); err != nil {
			return nil, fmt.Errorf("touch session: %w", err)
		}
	}
	return &Session{Kind: sess.Kind, Person: p, DeviceID: sess.DeviceID, TokenHash: hash}, nil
}

// EndSession signs out / locks the session for token.
func (s *Service) EndSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.st.Q.DeleteSession(ctx, HashToken(token))
}

// RevokeSession ends another personal session by its token hash.
func (s *Service) RevokeSession(ctx context.Context, householdID int64, tokenHash []byte, actorID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		n, err := q.DeleteSessionForHousehold(ctx, dbq.DeleteSessionForHouseholdParams{HouseholdID: householdID, TokenHash: tokenHash})
		if err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		if n == 0 {
			return nil
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "session.revoked",
			SubjectType: "person", SubjectID: actorID, At: s.now(),
		})
	})
}

// SignOutEverywhere ends every session for personID except keep (the
// caller's own, which may be nil to end all).
func (s *Service) SignOutEverywhere(ctx context.Context, householdID, personID int64, keep []byte) error {
	if keep == nil {
		keep = []byte{}
	}
	return s.st.Q.DeleteOtherSessionsForPerson(ctx, dbq.DeleteOtherSessionsForPersonParams{
		HouseholdID: householdID, PersonID: personID, KeepTokenHash: keep,
	})
}

// RevokeDevice unregisters a household display and ends its admin sessions.
func (s *Service) RevokeDevice(ctx context.Context, householdID, deviceID, actorID int64) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		n, err := q.RevokeDevice(ctx, dbq.RevokeDeviceParams{RevokedAt: &[]int64{now.Unix()}[0], HouseholdID: householdID, ID: deviceID})
		if err != nil {
			return fmt.Errorf("revoke device: %w", err)
		}
		if n == 0 {
			return nil
		}
		if err := q.DeleteSessionsForDevice(ctx, &deviceID); err != nil {
			return fmt.Errorf("delete device sessions: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "device.revoked",
			SubjectType: "device", SubjectID: deviceID, At: now,
		})
	})
}

// Sweep deletes expired sessions for householdID.
func (s *Service) Sweep(ctx context.Context, householdID int64) (int64, error) {
	set, err := s.settings.Get(ctx, householdID)
	if err != nil {
		return 0, err
	}
	now := s.now()
	return s.st.Q.DeleteExpiredSessions(ctx, dbq.DeleteExpiredSessionsParams{
		HouseholdID:    householdID,
		PersonalBefore: now.Add(-PersonalSessionTTL).Unix(),
		DisplayBefore:  now.Add(-set.AdminIdle()).Unix(),
	})
}

func pinKey(personID int64) string { return "pin:" + strconv.FormatInt(personID, 10) }
