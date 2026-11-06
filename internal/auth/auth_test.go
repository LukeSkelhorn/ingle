package auth_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type fixture struct {
	t    *testing.T
	st   *store.Store
	svc  *auth.Service
	hh   int64
	now  time.Time
	mum  dbq.Person
	kid  dbq.Person
	disp *dbq.Device
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	f := &fixture{t: t, st: st, now: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)}
	h, err := st.EnsureHousehold(ctx, "Home", f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.hh = h.ID
	set := settings.New(st)
	if err := set.Ensure(ctx, h.ID, time.UTC); err != nil {
		t.Fatal(err)
	}
	f.svc = auth.New(st, set)
	f.svc.SetClock(func() time.Time { return f.now })

	f.mum = f.person("Mum", "adult")
	pin, _ := auth.HashPIN("2468")
	pw, _ := auth.HashPassword("correct horse battery")
	email := "mum@example.com"
	must(t, st.Q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &pin, HouseholdID: f.hh, ID: f.mum.ID}))
	must(t, st.Q.SetPersonLogin(ctx, dbq.SetPersonLoginParams{Email: &email, PasswordHash: &pw, HouseholdID: f.hh, ID: f.mum.ID}))
	f.kid = f.person("Alfie", "child")
	kidPIN, _ := auth.HashPIN("1111")
	must(t, st.Q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &kidPIN, HouseholdID: f.hh, ID: f.kid.ID}))

	tok, dev, err := f.svc.RegisterDisplay(ctx, f.hh, "Kitchen", f.mum.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Display(ctx, tok)
	if err != nil || got == nil || got.ID != dev.ID {
		t.Fatalf("Display(token) = %v, %v", got, err)
	}
	f.disp = got
	return f
}

func (f *fixture) person(name, kind string) dbq.Person {
	p, err := f.st.Q.CreatePerson(f.t.Context(), dbq.CreatePersonParams{
		HouseholdID: f.hh, Name: name, Kind: kind, Colour: "#4A90D9", CreatedAt: 1,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPINUnlockNeedsDisplay(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.UnlockWithPIN(ctx, nil, f.mum.ID, "2468"); !errors.Is(err, auth.ErrNotDisplay) {
		t.Errorf("no display: %v", err)
	}
	if _, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "0000"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("wrong PIN: %v", err)
	}
	// Kids can't unlock admin even with a PIN in the database.
	if _, err := f.svc.UnlockWithPIN(ctx, f.disp, f.kid.ID, "1111"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("kid: %v", err)
	}
	tok, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "2468")
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.svc.Session(ctx, tok, f.disp)
	if err != nil || s == nil || s.Person.ID != f.mum.ID || s.Kind != auth.KindDisplay {
		t.Fatalf("Session = %+v, %v", s, err)
	}
	// Not valid without the display, or on another display.
	if s, _ := f.svc.Session(ctx, tok, nil); s != nil {
		t.Error("display session valid without the display")
	}
	other := &dbq.Device{ID: f.disp.ID + 99, HouseholdID: f.hh}
	if s, _ := f.svc.Session(ctx, tok, other); s != nil {
		t.Error("display session valid on another display")
	}
}

func TestDisplaySessionIdleExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tok, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "2468")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.now = f.now.Add(100 * time.Second) // under the 2-minute default
		if s, _ := f.svc.Session(ctx, tok, f.disp); s == nil {
			t.Fatal("expired while active")
		}
	}
	f.now = f.now.Add(2 * time.Minute)
	if s, _ := f.svc.Session(ctx, tok, f.disp); s != nil {
		t.Fatal("still valid after idle timeout")
	}
}

func TestSignInAndPersonalExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, _, err := f.svc.SignIn(ctx, "nobody@example.com", "correct horse battery", "ua", "1.2.3.4"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("unknown email: %v", err)
	}
	tok, p, err := f.svc.SignIn(ctx, " Mum@Example.com ", "correct horse battery", "Safari", "1.2.3.4")
	if err != nil || p.ID != f.mum.ID {
		t.Fatalf("SignIn = %v, %v", p.ID, err)
	}
	s, err := f.svc.Session(ctx, tok, nil)
	if err != nil || s == nil || s.Kind != auth.KindPersonal {
		t.Fatalf("Session = %+v, %v", s, err)
	}
	f.now = f.now.Add(auth.PersonalSessionTTL - time.Hour)
	if s, _ := f.svc.Session(ctx, tok, nil); s == nil {
		t.Fatal("expired early")
	}
	f.now = f.now.Add(auth.PersonalSessionTTL)
	if s, _ := f.svc.Session(ctx, tok, nil); s != nil {
		t.Fatal("valid after 30 idle days")
	}
}

func TestRevokeDeviceEndsItsSessions(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tok, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "2468")
	if err != nil {
		t.Fatal(err)
	}
	must(t, f.svc.RevokeDevice(ctx, f.hh, f.disp.ID, f.mum.ID))
	if s, _ := f.svc.Session(ctx, tok, f.disp); s != nil {
		t.Error("session survived device revocation")
	}
	n, err := f.st.Q.ListDevices(ctx, f.hh)
	if err != nil || len(n) != 0 {
		t.Errorf("devices after revoke = %v, %v", n, err)
	}
}

func TestArchivedPersonSessionInvalid(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tok, _, err := f.svc.SignIn(ctx, "mum@example.com", "correct horse battery", "", "")
	if err != nil {
		t.Fatal(err)
	}
	at := f.now.Unix()
	must(t, f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: &at, HouseholdID: f.hh, ID: f.mum.ID}))
	if s, _ := f.svc.Session(ctx, tok, nil); s != nil {
		t.Error("archived person still signed in")
	}
	if _, _, err := f.svc.SignIn(ctx, "mum@example.com", "correct horse battery", "", ""); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("archived sign-in: %v", err)
	}
}

func TestSweep(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "2468"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.SignIn(ctx, "mum@example.com", "correct horse battery", "", ""); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Hour)
	if n, err := f.svc.Sweep(ctx, f.hh); err != nil || n != 1 {
		t.Errorf("Sweep after 1h = %d, %v; want the display session", n, err)
	}
	f.now = f.now.Add(auth.PersonalSessionTTL)
	if n, err := f.svc.Sweep(ctx, f.hh); err != nil || n != 1 {
		t.Errorf("Sweep after 30d = %d, %v; want the personal session", n, err)
	}
}
