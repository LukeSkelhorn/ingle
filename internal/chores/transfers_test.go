package chores

import (
	"errors"
	"slices"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func TestTransferApproved(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 20, 800) // 20 pts, £8.00

	req, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	// The £5.00 is held: £3.00 left to spend.
	if s := f.summary(f.kid); s.Pence != 300 || s.HeldPence != 500 || s.Waiting != 0 || s.Points != 20 {
		t.Errorf("summary while waiting = %+v", s)
	}
	// So a £4.00 reward can't be claimed, and nor can another request.
	toy := f.reward("Toy", 0, 400)
	if _, err := f.svc.ClaimReward(ctx, f.hh, toy.ID, f.kid.ID, nil); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("claim while held = %v, want ErrInsufficientBalance", err)
	}
	if _, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 100); !errors.Is(err, ErrTransferPending) {
		t.Errorf("second request = %v, want ErrTransferPending", err)
	}
	b, err := f.svc.BankStatus(ctx, f.hh, f.kid.ID, f.svc.now().AddDate(0, 0, -7))
	if err != nil || b.Waiting == nil || b.Waiting.ID != req.ID || b.Declined != nil {
		t.Errorf("bank status = %+v, %v", b, err)
	}
	if n, _ := f.svc.PendingTransferCount(ctx, f.hh); n != 1 {
		t.Errorf("pending count = %d", n)
	}

	if err := f.svc.ApproveTransfer(ctx, f.hh, req.ID, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("approved by a child = %v", err)
	}
	if err := f.svc.ApproveTransfer(ctx, f.hh, req.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveTransfer(ctx, f.hh, req.ID, f.adult.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("approved twice = %v, want ErrNotPending", err)
	}
	// One payout, linked, and the balance is £3.00 with nothing held.
	if s := f.summary(f.kid); s.Pence != 300 || s.HeldPence != 0 {
		t.Errorf("summary after approval = %+v", s)
	}
	got, err := f.st.Q.GetTransferRequest(ctx, dbq.GetTransferRequestParams{HouseholdID: f.hh, ID: req.ID})
	if err != nil || got.Status != StatusApproved || got.LedgerEntryID == nil {
		t.Fatalf("request after approval = %+v, %v", got, err)
	}
	items, _ := f.history(f.kid, 0)
	var payouts []HistoryItem
	for _, it := range items {
		if it.Kind == "payout" {
			payouts = append(payouts, it)
		}
	}
	if len(payouts) != 1 || payouts[0].ID != *got.LedgerEntryID || payouts[0].Pence != -500 || payouts[0].Points != 0 {
		t.Errorf("payouts = %+v", payouts)
	}
	if this, _ := f.svc.PersonSummary(ctx, f.hh, f.kid.ID, f.svc.now().AddDate(0, 0, -1)); this.WeekPence != 800 {
		t.Errorf("week pence = %d: a payout isn't earning less", this.WeekPence)
	}
	f.wantActivity("transfer.requested", "transfer.approved")
}

func TestTransferDeclinedAndCancelled(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 0, 1000)

	req, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var verr valid.Errors
	if err := f.svc.DeclineTransfer(ctx, f.hh, req.ID, f.adult.ID, "  "); !errors.As(err, &verr) || verr["reason"] == "" {
		t.Errorf("decline without a reason = %v", err)
	}
	if err := f.svc.DeclineTransfer(ctx, f.hh, req.ID, f.adult.ID, "Let's talk about it"); err != nil {
		t.Fatal(err)
	}
	if s := f.summary(f.kid); s.Pence != 1000 || s.HeldPence != 0 {
		t.Errorf("summary after decline = %+v", s)
	}
	b, err := f.svc.BankStatus(ctx, f.hh, f.kid.ID, f.svc.now().AddDate(0, 0, -7))
	if err != nil || b.Waiting != nil || b.Declined == nil || b.Declined.Reason != "Let's talk about it" {
		t.Errorf("bank status after decline = %+v, %v", b, err)
	}

	// Asking again hides the old reason; only they can cancel it.
	req, err = f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 250)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelTransfer(ctx, f.hh, f.kid2.ID, req.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancelled by someone else = %v", err)
	}
	if err := f.svc.CancelTransfer(ctx, f.hh, f.kid.ID, req.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelTransfer(ctx, f.hh, f.kid.ID, req.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("cancelled twice = %v", err)
	}
	if b, _ := f.svc.BankStatus(ctx, f.hh, f.kid.ID, f.svc.now().AddDate(0, 0, -7)); b.Waiting != nil || b.Declined != nil {
		t.Errorf("bank status after cancel = %+v", b)
	}
	if s := f.summary(f.kid); s.Pence != 1000 {
		t.Errorf("pence after cancel = %d", s.Pence)
	}
	f.wantActivity("transfer.requested", "transfer.declined", "transfer.requested", "transfer.cancelled")
}

func TestTransferRefused(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 0, 300)
	for _, pence := range []int64{0, -50, 301} {
		if _, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, pence); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("request %dp = %v, want ErrInsufficientBalance", pence, err)
		}
		if _, err := f.svc.PayOut(ctx, f.hh, f.kid.ID, pence, f.adult.ID); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("pay out %dp = %v, want ErrInsufficientBalance", pence, err)
		}
	}
	// A waiting reward claim holds its pence too.
	toy := f.reward("Toy", 0, 200)
	if _, err := f.svc.ClaimReward(ctx, f.hh, toy.ID, f.kid.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RequestTransfer(ctx, f.hh, f.kid.ID, 150); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("request over a held claim = %v", err)
	}
	if _, err := f.svc.PayOut(ctx, f.hh, f.kid.ID, 100, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("pay out by a child = %v", err)
	}
}

func TestPayOut(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 0, 750)

	tr, err := f.svc.PayOut(ctx, f.hh, f.kid.ID, 750, f.adult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusApproved {
		t.Errorf("status = %q", tr.Status)
	}
	if s := f.summary(f.kid); s.Pence != 0 {
		t.Errorf("pence after paying out = %d", s.Pence)
	}
	list, err := f.svc.Transfers(ctx, f.hh, f.svc.now().AddDate(0, 0, -30))
	if err != nil || len(list) != 1 || list[0].RequestedBy != f.adult.Name || list[0].ReviewedBy != f.adult.Name || list[0].Pending() {
		t.Errorf("transfers = %+v, %v", list, err)
	}
	if n, _ := f.svc.PendingTransferCount(ctx, f.hh); n != 0 {
		t.Errorf("pending count = %d", n)
	}
	f.wantActivity("transfer.paid")
}

// wantActivity checks the transfer activity logged, oldest first.
func (f *fixture) wantActivity(want ...string) {
	f.t.Helper()
	acts, err := f.st.Q.ListRecentActivity(f.t.Context(), dbq.ListRecentActivityParams{HouseholdID: f.hh, Limit: 50})
	if err != nil {
		f.t.Fatal(err)
	}
	var got []string
	for _, a := range slices.Backward(acts) {
		if a.SubjectType == "transfer" {
			got = append(got, a.Verb)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Errorf("transfer activity = %v, want %v", got, want)
	}
}
