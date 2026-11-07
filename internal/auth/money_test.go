package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestMoneyPIN(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// A child's PIN opens their money, and only on a display.
	if _, err := f.svc.CheckMoneyPIN(ctx, nil, f.kid.ID, "1111", "a"); !errors.Is(err, auth.ErrNotDisplay) {
		t.Errorf("no display: %v", err)
	}
	tok, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "1111", "a")
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := f.svc.MoneyToken(f.disp, tok); !ok || who != f.kid.ID {
		t.Errorf("MoneyToken = %d, %v", who, ok)
	}
	// An adult's PIN opens their own money too.
	mumTok, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.mum.ID, "2468", "a")
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := f.svc.MoneyToken(f.disp, mumTok); !ok || who != f.mum.ID {
		t.Errorf("mum's MoneyToken = %d, %v", who, ok)
	}
	// Someone else's PIN doesn't.
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "2468", "a"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("mum's PIN for Alfie: %v", err)
	}

	// The token is for that display only…
	_, other, err := f.svc.RegisterDisplay(ctx, f.hh, "Hall", f.mum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.svc.MoneyToken(&other, tok); ok {
		t.Error("token works on another display")
	}
	if _, ok := f.svc.MoneyToken(nil, tok); ok {
		t.Error("token works off a display")
	}
	if _, ok := f.svc.MoneyToken(f.disp, "nonsense"); ok {
		t.Error("made-up token works")
	}
	// …and for a minute.
	f.now = f.now.Add(auth.MoneyTokenTTL - time.Second)
	if _, ok := f.svc.MoneyToken(f.disp, tok); !ok {
		t.Error("token expired early")
	}
	f.now = f.now.Add(time.Second)
	if _, ok := f.svc.MoneyToken(f.disp, tok); ok {
		t.Error("token outlives its minute")
	}

	// A child's PIN never opens admin or unlocks the displays.
	if _, err := f.svc.UnlockWithPIN(ctx, f.disp, f.kid.ID, "1111"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("kid admin unlock: %v", err)
	}
	if err := f.svc.CheckUnlockPIN(ctx, f.disp, f.kid.ID, "1111", "b"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("kid display unlock: %v", err)
	}
	if who, err := f.svc.CheckUnlockCode(ctx, f.disp, "1111", "b"); err == nil {
		t.Errorf("kid PIN as the unlock code: %v", who)
	}
}

func TestMoneyPINRefused(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	nobody := f.person("Isla", "child")
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, nobody.ID, "1111", "a"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("no PIN: %v", err)
	}
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, 9999, "1111", "a"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("unknown person: %v", err)
	}
	must(t, f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: new(int64(1)), HouseholdID: f.hh, ID: f.kid.ID}))
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "1111", "a"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("archived: %v", err)
	}
}

func TestMoneyPINLockout(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for i := 1; i < auth.MaxFailures; i++ {
		if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "0000", "a"); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "0000", "a"); !errors.Is(err, auth.ErrLockedOut) {
		t.Fatalf("5th wrong PIN: %v", err)
	}
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "1111", "a"); !errors.Is(err, auth.ErrLockedOut) {
		t.Errorf("right PIN while locked out: %v", err)
	}
	f.now = f.now.Add(auth.LockoutPeriod)
	if _, err := f.svc.CheckMoneyPIN(ctx, f.disp, f.kid.ID, "1111", "a"); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}
