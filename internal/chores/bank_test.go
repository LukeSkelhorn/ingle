package chores

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func (f *fixture) bank(p dbq.Person) *Bank {
	f.t.Helper()
	b, err := f.svc.PersonBank(f.t.Context(), f.hh, p.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func TestSetBank(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if b := f.bank(f.kid); b != nil {
		t.Fatalf("bank before it's set up = %+v", b)
	}
	var verr valid.Errors
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, " ", -1, f.adult.ID); !errors.As(err, &verr) || verr["bank_label"] == "" || verr["bank_pence"] == "" {
		t.Errorf("bad input = %v", err)
	}
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, "Monzo", 2340, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("set by a child = %v", err)
	}
	if err := f.svc.SetBank(ctx, f.hh, f.adult.ID, "Monzo", 2340, f.adult.ID); !errors.Is(err, ErrNotChild) {
		t.Errorf("set for an adult = %v", err)
	}
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, " Monzo ", 2340, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	set := f.svc.now()
	if b := f.bank(f.kid); b == nil || b.Label != "Monzo" || b.Pence != 2340 || b.Source != BankManual || !b.UpdatedAt.Equal(set) {
		t.Errorf("bank = %+v", b)
	}
	if b := f.bank(f.kid2); b != nil {
		t.Errorf("kid2's bank = %+v", b)
	}

	// Fresh: no nudge. A fortnight and a day on, there is.
	if s, _ := f.svc.StaleBanks(ctx, f.hh); len(s) != 0 {
		t.Errorf("stale when fresh = %+v", s)
	}
	f.svc.now = func() time.Time { return set.Add(BankStale + 24*time.Hour) }
	s, err := f.svc.StaleBanks(ctx, f.hh)
	if err != nil || len(s) != 1 || s[0].PersonID != f.kid.ID || s[0].Name != f.kid.Name {
		t.Errorf("stale = %+v, %v", s, err)
	}
	// Updating, even to the same amount, clears it.
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, "Monzo", 2340, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.svc.StaleBanks(ctx, f.hh); len(s) != 0 {
		t.Errorf("stale after update = %+v", s)
	}

	// Archived: no nudge, and it can't be changed.
	f.svc.now = func() time.Time { return set.Add(2*BankStale + 24*time.Hour) }
	at := f.svc.now().Unix()
	if err := f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: &at, HouseholdID: f.hh, ID: f.kid.ID}); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.svc.StaleBanks(ctx, f.hh); len(s) != 0 {
		t.Errorf("stale while archived = %+v", s)
	}
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, "Monzo", 0, f.adult.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("set while archived = %v", err)
	}

	if err := f.svc.RemoveBank(ctx, f.hh, f.kid.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if b := f.bank(f.kid); b != nil {
		t.Errorf("bank after remove = %+v", b)
	}
	if err := f.svc.RemoveBank(ctx, f.hh, f.kid.ID, f.adult.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed twice = %v", err)
	}
	f.wantVerbs("bank.updated", "bank.updated", "bank.removed")
}

func TestPayoutAddsToBank(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 0, 1000)
	f.earn(f.kid2, 0, 1000)
	if err := f.svc.SetBank(ctx, f.hh, f.kid.ID, "Monzo", 2000, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	checked := f.svc.now()
	f.svc.now = func() time.Time { return checked.Add(time.Hour) }

	req, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 300)
	if err != nil {
		t.Fatal(err)
	}
	if b := f.bank(f.kid); b.Pence != 2000 {
		t.Errorf("bank while waiting = %d", b.Pence)
	}
	if err := f.svc.ApproveTransfer(ctx, f.hh, req.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PayOut(ctx, f.hh, f.kid.ID, 200, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	// It's added on, but nobody has checked the real bank since.
	if b := f.bank(f.kid); b.Pence != 2500 || !b.UpdatedAt.Equal(checked) {
		t.Errorf("bank after payouts = %+v", b)
	}
	// No bank set up: payouts still work, and none appears.
	if _, err := f.svc.PayOut(ctx, f.hh, f.kid2.ID, 200, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if b := f.bank(f.kid2); b != nil {
		t.Errorf("kid2's bank = %+v", b)
	}
}

// wantVerbs checks the bank activity logged, oldest first.
func (f *fixture) wantVerbs(want ...string) {
	f.t.Helper()
	var got []string
	for _, v := range slices.Backward(f.verbs()) {
		if strings.HasPrefix(v, "bank.") {
			got = append(got, v)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Errorf("bank activity = %v, want %v", got, want)
	}
}
