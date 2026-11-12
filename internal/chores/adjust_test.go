package chores

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func TestAdjustPoints(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 3, 50)

	// Taking away more than they have goes below zero; money is untouched.
	e, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, -6, "  Hit his sister. We don't hurt people. ")
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "manual" || e.Note != "Hit his sister. We don't hurt people." || *e.CreatedBy != f.adult.ID {
		t.Errorf("entry = %+v", e)
	}
	s := f.summary(f.kid)
	if s.Points != -3 || s.Pence != 50 {
		t.Errorf("balance = %d pts %dp, want -3 pts 50p", s.Points, s.Pence)
	}
	if s.WeekPoints != -3 {
		t.Errorf("this week = %d, want net -3", s.WeekPoints)
	}

	// A bonus, for an adult too.
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.adult.ID, 2, "Did the big shop"); err != nil {
		t.Fatal(err)
	}
	if got := f.summary(f.adult).Points; got != 2 {
		t.Errorf("adult balance = %d, want 2", got)
	}

	// A chore reversal isn't listed as an adjustment.
	if _, err := f.st.Q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID: f.hh, PersonID: f.kid.ID, Points: -1, Kind: "adjustment", Note: "Reversed: Bins", CreatedAt: f.svc.now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	list, err := f.svc.Adjustments(ctx, f.hh, f.kid.ID, f.svc.now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Points != -6 || list[0].By != "Mum" || list[0].ID != e.ID {
		t.Errorf("adjustments = %+v", list)
	}
	if notes, _ := f.svc.ReasonSuggestions(ctx, f.hh); len(notes) != 2 {
		t.Errorf("suggestions = %q", notes)
	}
}

func TestAdjustPointsKeepsHolds(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earn(f.kid, 30, 0)
	screen := f.reward("Screen time", 20, 0)
	if _, err := f.svc.ClaimReward(ctx, f.hh, screen.ID, f.kid.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, -15, "Shouted at Gran"); err != nil {
		t.Fatal(err)
	}
	s := f.summary(f.kid)
	if s.Points != -5 || s.HeldPoints != 20 || s.Waiting != 1 {
		t.Errorf("summary = %d free, %d held, %d waiting; want -5, 20, 1", s.Points, s.HeldPoints, s.Waiting)
	}
}

func TestAdjustPointsRefuses(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.kid.ID, f.kid2.ID, -5, "Took my Lego"); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child as adult: err = %v, want ErrNotAdult", err)
	}
	for _, tc := range []struct {
		points int64
		note   string
		field  string
	}{
		{0, "Something", "amount"},
		{1001, "Something", "amount"},
		{-1001, "Something", "amount"},
		{5, "   ", "reason"},
		{5, strings.Repeat("x", 201), "reason"},
	} {
		_, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, tc.points, tc.note)
		var ve valid.Errors
		if !errors.As(err, &ve) || ve[tc.field] == "" {
			t.Errorf("AdjustPoints(%d, %q) err = %v, want a %s error", tc.points, tc.note, err, tc.field)
		}
	}
	if _, err := f.st.SQL().ExecContext(ctx, "UPDATE people SET archived_at = 1 WHERE id = ?", f.kid2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid2.ID, 5, "Tidied up"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived: err = %v, want ErrNotFound", err)
	}
}

func TestUndoAdjustment(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	e, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, -5, "Hit his sister")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.UndoAdjustment(ctx, f.hh, f.kid.ID, e.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child undo: err = %v, want ErrNotAdult", err)
	}
	if err := f.svc.UndoAdjustment(ctx, f.hh, f.adult.ID, e.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.summary(f.kid).Points; got != 0 {
		t.Errorf("after undo balance = %d, want 0", got)
	}
	if err := f.svc.UndoAdjustment(ctx, f.hh, f.adult.ID, e.ID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("undo twice: err = %v, want ErrUndoExpired", err)
	}

	// Too old.
	old, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, 2, "Helped")
	if err != nil {
		t.Fatal(err)
	}
	now := f.svc.now()
	f.svc.now = func() time.Time { return now.Add(AdjustmentUndoWindow + time.Second) }
	if err := f.svc.UndoAdjustment(ctx, f.hh, f.adult.ID, old.ID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("old undo: err = %v, want ErrUndoExpired", err)
	}

	// Only manual entries can be undone this way.
	f.earn(f.kid, 4, 0)
	var id int64
	if err := f.st.SQL().QueryRowContext(ctx, "SELECT MAX(id) FROM ledger_entries WHERE kind = 'completion'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.UndoAdjustment(ctx, f.hh, f.adult.ID, id); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("completion undo: err = %v, want ErrUndoExpired", err)
	}
}
