package chores

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

func (f *fixture) reward(title string, points, pence int64) dbq.Reward {
	f.t.Helper()
	r, err := f.st.Q.CreateReward(f.t.Context(), dbq.CreateRewardParams{
		HouseholdID: f.hh, Title: title, CostPoints: points, CostPence: pence, CreatedAt: 1,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) earn(p dbq.Person, points, pence int64) {
	f.t.Helper()
	if _, err := f.st.Q.InsertLedgerEntry(f.t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: f.hh, PersonID: p.ID, Points: points, Pence: pence, Kind: "completion", CreatedAt: f.svc.now().Unix(),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) summary(p dbq.Person) Summary {
	f.t.Helper()
	s, err := f.svc.PersonSummary(f.t.Context(), f.hh, p.ID, time.Date(2026, 11, 2, 0, 0, 0, 0, london))
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) ledgerRows(p dbq.Person) int {
	f.t.Helper()
	var n int
	if err := f.st.DB.QueryRowContext(f.t.Context(),
		"SELECT COUNT(*) FROM ledger_entries WHERE person_id = ? AND kind = 'redemption'", p.ID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestClaimHoldsUntilApproved(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 70, 100)
	screen := f.reward("Screen time", 30, 0)
	comic := f.reward("Comic", 0, 150)

	// Two of the same reward is fine while they can afford both.
	a, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != StatusPending {
		t.Errorf("status = %q, want pending", a.Status)
	}
	b, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := f.summary(f.kid)
	if s.Points != 10 || s.HeldPoints != 60 || s.Waiting != 2 {
		t.Errorf("summary = %d available, %d held, %d waiting; want 10, 60, 2", s.Points, s.HeldPoints, s.Waiting)
	}
	if s.WeekPoints != 70 {
		t.Errorf("this week = %d, want 70 (claims aren't earnings)", s.WeekPoints)
	}
	// The third would spend held points.
	if _, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, nil); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("third claim: err = %v, want ErrInsufficientBalance", err)
	}
	// Money counts separately.
	if _, err := f.svc.ClaimReward(ctx, f.hh, comic.ID, f.kid.ID, nil); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("comic: err = %v, want ErrInsufficientBalance", err)
	}
	if n := f.ledgerRows(f.kid); n != 0 {
		t.Errorf("ledger has %d redemption rows before approval", n)
	}

	// Only an adult approves.
	if err := f.svc.ApproveRedemption(ctx, f.hh, a.ID, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child approving: err = %v, want ErrNotAdult", err)
	}
	if err := f.svc.ApproveRedemption(ctx, f.hh, a.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveRedemption(ctx, f.hh, a.ID, f.adult.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("second approve: err = %v, want ErrNotPending", err)
	}
	if bal := f.balance(f.kid); bal.Points != 40 {
		t.Errorf("balance = %d, want 40", bal.Points)
	}
	if n := f.ledgerRows(f.kid); n != 1 {
		t.Errorf("ledger has %d redemption rows, want 1", n)
	}

	// Declining lets the hold go, and touches nothing else.
	if err := f.svc.DeclineRedemption(ctx, f.hh, b.ID, f.kid.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child declining: err = %v, want ErrNotAdult", err)
	}
	if err := f.svc.DeclineRedemption(ctx, f.hh, b.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	s = f.summary(f.kid)
	if s.Points != 40 || s.HeldPoints != 0 || s.Waiting != 0 {
		t.Errorf("after decline = %d available, %d held, %d waiting; want 40, 0, 0", s.Points, s.HeldPoints, s.Waiting)
	}
	if err := f.svc.CancelRedemption(ctx, f.hh, b.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("cancel declined: err = %v, want ErrNotPending", err)
	}

	verbs := f.verbs()
	want := []string{"reward.declined", "reward.redeemed", "reward.requested", "reward.requested"}
	if !slices.Equal(verbs[:4], want) {
		t.Errorf("activity = %v, want %v first", verbs, want)
	}
}

func TestCancelClaim(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 30, 0)
	screen := f.reward("Screen time", 30, 0)
	c, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelRedemption(ctx, f.hh+1, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household: err = %v, want ErrNotFound", err)
	}
	if err := f.svc.CancelRedemption(ctx, f.hh, c.ID); err != nil {
		t.Fatal(err)
	}
	if s := f.summary(f.kid); s.Points != 30 || s.Waiting != 0 {
		t.Errorf("after cancel = %d available, %d waiting", s.Points, s.Waiting)
	}
	d, err := f.st.Q.GetRedemption(ctx, dbq.GetRedemptionParams{HouseholdID: f.hh, ID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusRejected || d.ReviewedBy == nil || *d.ReviewedBy != f.kid.ID {
		t.Errorf("cancelled = %+v, want rejected by the claimant", d)
	}
	if n := f.ledgerRows(f.kid); n != 0 {
		t.Errorf("ledger has %d redemption rows", n)
	}
}

func TestClaimWithAdultThere(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 30, 0)
	screen := f.reward("Screen time", 30, 0)

	if _, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, &f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child approver: err = %v, want ErrNotAdult", err)
	}
	c, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, &f.adult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusApproved {
		t.Errorf("status = %q, want approved", c.Status)
	}
	if s := f.summary(f.kid); s.Points != 0 || s.Waiting != 0 {
		t.Errorf("after = %d available, %d waiting; want 0, 0", s.Points, s.Waiting)
	}
	if n := f.ledgerRows(f.kid); n != 1 {
		t.Errorf("ledger has %d redemption rows, want 1", n)
	}
}

func TestClaimRefused(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 100, 0)
	gone := f.reward("Gone", 1, 0)
	if err := f.st.Q.ArchiveReward(ctx, dbq.ArchiveRewardParams{HouseholdID: f.hh, ID: gone.ID, ArchivedAt: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClaimReward(ctx, f.hh, gone.ID, f.kid.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived reward: err = %v, want ErrNotFound", err)
	}
	ok := f.reward("Fine", 1, 0)
	if _, err := f.svc.ClaimReward(ctx, f.hh+1, ok.ID, f.kid.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.ClaimReward(ctx, f.hh, ok.ID, f.kid2.ID, nil); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("nothing earned: err = %v, want ErrInsufficientBalance", err)
	}
}

func TestPendingRedemptions(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 10, 0)
	f.earn(f.kid2, 10, 0)
	r := f.reward("Sticker", 5, 0)
	for _, p := range []dbq.Person{f.kid, f.kid2, f.kid} {
		if _, err := f.svc.ClaimReward(ctx, f.hh, r.ID, p.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	all, err := f.svc.PendingRedemptions(ctx, f.hh, nil)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := f.svc.PendingRedemptions(ctx, f.hh, &f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || len(mine) != 2 || mine[0].Title != "Sticker" || mine[0].Person.Name != "Alfie" || !mine[0].Pending() {
		t.Errorf("all = %d, mine = %+v", len(all), mine)
	}
	one, err := f.svc.Redemption(ctx, f.hh, mine[1].ID)
	if err != nil || one.ID != mine[1].ID {
		t.Errorf("Redemption = %+v, %v", one, err)
	}
	if _, err := f.svc.Redemption(ctx, f.hh+1, mine[1].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household: err = %v", err)
	}
}

// verbs is the activity feed, newest first.
func (f *fixture) verbs() []string {
	f.t.Helper()
	acts, err := f.st.Q.ListRecentActivity(f.t.Context(), dbq.ListRecentActivityParams{HouseholdID: f.hh, Limit: 20})
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.Verb)
	}
	return out
}
