package chores

import (
	"errors"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

func TestReverseApproved(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Hoover", choreOpts{startsOn: "2026-11-02", points: 5, pence: 50})
	occ := f.onlyOccurrence()
	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Well past the kid's undo window, but within the adult's.
	base := f.svc.now()
	f.svc.now = func() time.Time { return base.Add(3 * 24 * time.Hour) }
	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Fatalf("child Reverse = %v, want ErrNotAdult", err)
	}
	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if b := f.balance(f.kid); b.Points != 0 || b.Pence != 0 {
		t.Errorf("balance = %+v, want zero", b)
	}
	got, err := f.st.Q.GetCompletion(ctx, dbq.GetCompletionParams{HouseholdID: f.hh, ID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRejected || got.ReviewedBy == nil || *got.ReviewedBy != f.adult.ID {
		t.Errorf("completion = %+v, want rejected by Mum", got)
	}
	// The original credit stays; a compensating adjustment sits beside it.
	var credits, adjustments int
	if err := f.st.DB.QueryRowContext(ctx,
		`SELECT
		   (SELECT count(*) FROM ledger_entries WHERE completion_id = ?),
		   (SELECT count(*) FROM ledger_entries WHERE kind = 'adjustment' AND points = -5 AND pence = -50 AND created_by = ? AND note = 'Reversed: Hoover')`,
		c.ID, f.adult.ID).Scan(&credits, &adjustments); err != nil {
		t.Fatal(err)
	}
	if credits != 1 || adjustments != 1 {
		t.Errorf("ledger: %d credits, %d adjustments; want 1 and 1", credits, adjustments)
	}
	if o := f.onlyOccurrence(); o.Status != StatusClaimed || o.ClaimedBy == nil || *o.ClaimedBy != f.kid.ID {
		t.Errorf("occurrence = %+v, want claimed by Alfie again", o)
	}

	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.adult.ID); !errors.Is(err, ErrNotCounted) {
		t.Errorf("second Reverse = %v, want ErrNotCounted", err)
	}
	if err := f.svc.Undo(ctx, f.hh, c.ID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("Undo after Reverse = %v, want ErrUndoExpired", err)
	}

	// It can be done again, and counts again.
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatalf("Complete again: %v", err)
	}
	if b := f.balance(f.kid); b.Points != 5 {
		t.Errorf("points = %d after redoing, want 5", b.Points)
	}
}

func TestReversePendingRejects(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Checked", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 3, approval: true})
	occ := f.onlyOccurrence()
	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if o := f.onlyOccurrence(); o.Status != StatusOpen {
		t.Errorf("status = %s, want open", o.Status)
	}
	var n int
	if err := f.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d ledger entries, want none", n)
	}
}

func TestReverseTooOld(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 2})
	c, err := f.svc.Complete(ctx, f.hh, f.onlyOccurrence().ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := f.svc.now()
	f.svc.now = func() time.Time { return base.Add(ReverseWindow + time.Minute) }
	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.adult.ID); !errors.Is(err, ErrTooOld) {
		t.Errorf("Reverse = %v, want ErrTooOld", err)
	}
	d, err := f.svc.Completion(ctx, f.hh, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.CanReverse {
		t.Error("CanReverse = true for an old completion")
	}
}

func TestReverseAfterSpendingGoesNegative(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Hoover", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 10})
	c, err := f.svc.Complete(ctx, f.hh, f.onlyOccurrence().ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	film, err := f.st.Q.CreateReward(ctx, dbq.CreateRewardParams{HouseholdID: f.hh, Title: "Film", CostPoints: 10, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Redeem(ctx, f.hh, film.ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reverse(ctx, f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if b := f.balance(f.kid); b.Points != -10 {
		t.Errorf("points = %d, want -10", b.Points)
	}
}

func TestCompletionsList(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Old", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 1})
	f.chore("Checked", choreOpts{assignee: &f.kid2.ID, startsOn: "2026-11-02", approval: true})
	f.chore("New", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 1})
	if _, err := f.svc.Materialise(ctx, f.hh, f.svc.now(), 1); err != nil {
		t.Fatal(err)
	}
	occs := map[string]int64{}
	for _, o := range f.board("2026-11-02", "2026-11-02") {
		occs[o.Title] = o.ID
	}
	base := f.svc.now()
	complete := func(title string, p dbq.Person, at time.Time) {
		f.svc.now = func() time.Time { return at }
		if _, err := f.svc.Complete(ctx, f.hh, occs[title], p.ID); err != nil {
			t.Fatal(err)
		}
	}
	complete("Old", f.kid, base)
	complete("Checked", f.kid2, base.Add(time.Hour))
	complete("New", f.kid, base.Add(2*time.Hour))

	// A week and an hour on: "Old" has aged out, pending always stays.
	f.svc.now = func() time.Time { return base.Add(ReverseWindow + time.Hour) }
	got, err := f.svc.Completions(ctx, f.hh)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Title)
	}
	if len(got) != 2 || got[0].Title != "Checked" || !got[0].Pending() || got[1].Title != "New" {
		t.Fatalf("completions = %v, want [Checked New]", names)
	}
	if got[0].CanReverse || !got[1].CanReverse || got[1].Person.Name != "Alfie" {
		t.Errorf("details = %+v", got)
	}
}
