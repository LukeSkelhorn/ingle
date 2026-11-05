package auth_test

import (
	"testing"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/settings"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

func (f *fixture) pinLength(id int64) *int64 {
	f.t.Helper()
	p, err := f.st.Q.GetPerson(f.t.Context(), dbq.GetPersonParams{HouseholdID: f.hh, ID: id})
	if err != nil {
		f.t.Fatal(err)
	}
	return p.PinLength
}

func (f *fixture) codeLength() int {
	f.t.Helper()
	set, err := settings.New(f.st).Get(f.t.Context(), f.hh) // fresh cache
	if err != nil {
		f.t.Fatal(err)
	}
	return set.UnlockCodeLength
}

// The fixture's PINs were stored without a length, like PINs set before
// lengths were kept. A successful unlock records it; a wrong one doesn't.
func TestPINLengthLearnedOnUnlock(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if got := f.pinLength(f.mum.ID); got != nil {
		t.Fatalf("pin_length before = %d, want NULL", *got)
	}
	_ = f.svc.CheckUnlockPIN(ctx, f.disp, f.mum.ID, "999999", "1.2.3.4")
	if got := f.pinLength(f.mum.ID); got != nil {
		t.Errorf("wrong PIN recorded length %d", *got)
	}
	if err := f.svc.CheckUnlockPIN(ctx, f.disp, f.mum.ID, "2468", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if got := f.pinLength(f.mum.ID); got == nil || *got != 4 {
		t.Errorf("pin_length after unlock = %v, want 4", got)
	}

	// Admin unlock learns it too.
	pin, _ := auth.HashPIN("135790")
	must(t, f.st.Q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &pin, HouseholdID: f.hh, ID: f.mum.ID}))
	if _, err := f.svc.UnlockWithPIN(ctx, f.disp, f.mum.ID, "135790"); err != nil {
		t.Fatal(err)
	}
	if got := f.pinLength(f.mum.ID); got == nil || *got != 6 {
		t.Errorf("pin_length after admin unlock = %v, want 6", got)
	}
}

func TestUnlockCodeLength(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	must(t, f.svc.SetUnlockCode(ctx, f.hh, f.mum.ID, "97531"))
	if got := f.codeLength(); got != 5 {
		t.Errorf("length after SetUnlockCode = %d, want 5", got)
	}

	// An older code without a length learns it on its first unlock.
	hash, _ := auth.HashPIN("864200")
	must(t, f.st.Q.SetUnlockCode(ctx, dbq.SetUnlockCodeParams{UnlockCodeHash: &hash, HouseholdID: f.hh}))
	svc := auth.New(f.st, settings.New(f.st)) // no cached settings
	if got := f.codeLength(); got != 0 {
		t.Fatalf("length before = %d, want 0", got)
	}
	if _, err := svc.CheckUnlockCode(ctx, f.disp, "864200", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if got := f.codeLength(); got != 6 {
		t.Errorf("length after unlock = %d, want 6", got)
	}

	must(t, f.svc.ClearUnlockCode(ctx, f.hh, f.mum.ID))
	if got := f.codeLength(); got != 0 {
		t.Errorf("length after clear = %d, want 0", got)
	}
}
