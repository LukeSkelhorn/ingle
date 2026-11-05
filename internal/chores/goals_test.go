package chores

import (
	"errors"
	"testing"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

func TestNewGoalProgress(t *testing.T) {
	cases := []struct {
		name                  string
		costPoints, costPence int64
		points, pence         int64
		waiting               bool
		wantPct               int
		wantPts, wantPence    int64
		wantReached           bool
	}{
		{"points", 200, 0, 120, 0, false, 60, 120, 0, false},
		{"money", 0, 500, 0, 125, false, 25, 0, 125, false},
		{"mixed is the slower part", 100, 200, 90, 50, false, 25, 90, 50, false},
		{"mixed, money done", 100, 200, 40, 900, false, 40, 40, 200, false},
		{"over is capped", 50, 0, 80, 0, false, 100, 50, 0, true},
		{"overdrawn is zero", 50, 0, -10, 0, false, 0, 0, 0, false},
		{"free", 0, 0, 0, 0, false, 100, 0, 0, true},
		{"waiting shows full, not reached", 200, 0, 0, 0, true, 100, 200, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w int64
			if c.waiting {
				w = 1
			}
			g := newGoal(dbq.ListGoalsRow{ID: 1, Title: "Lego", CostPoints: c.costPoints, CostPence: c.costPence, Waiting: w}, c.points, c.pence)
			if g.Percent != c.wantPct || g.HavePoints != c.wantPts || g.HavePence != c.wantPence || g.Reached != c.wantReached || g.Waiting != c.waiting {
				t.Errorf("goal = %d%% (%d pts, %dp) reached=%v waiting=%v; want %d%% (%d, %d) reached=%v",
					g.Percent, g.HavePoints, g.HavePence, g.Reached, g.Waiting, c.wantPct, c.wantPts, c.wantPence, c.wantReached)
			}
		})
	}
	if e := (Goal{}).Emoji(); e != DefaultGoalEmoji {
		t.Errorf("blank emoji = %q, want %q", e, DefaultGoalEmoji)
	}
}

func TestGoalLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	lego := f.reward("Lego", 100, 0)
	other := f.reward("Comic", 10, 0)
	f.earn(f.kid, 70, 0)

	if g := f.summary(f.kid).Goal; g != nil {
		t.Fatalf("goal before setting = %+v", g)
	}
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, lego.ID); err != nil {
		t.Fatal(err)
	}
	g := f.summary(f.kid).Goal
	if g == nil || g.RewardID != lego.ID || g.Percent != 70 || g.Reached || g.SetAt == 0 {
		t.Fatalf("goal = %+v, want Lego at 70%%", g)
	}

	// Holds count: a waiting claim for something else slows the goal down.
	comic, err := f.svc.ClaimReward(ctx, f.hh, other.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g.Percent != 60 {
		t.Errorf("with a hold, percent = %d, want 60", g.Percent)
	}
	// Approving a different reward leaves the goal alone.
	if err := f.svc.ApproveRedemption(ctx, f.hh, comic.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if f.summary(f.kid).Goal == nil {
		t.Fatal("approving another reward cleared the goal")
	}

	// Reached: it shows on the board too.
	f.earn(f.kid, 40, 0)
	if g := f.summary(f.kid).Goal; !g.Reached {
		t.Errorf("goal = %+v, want reached", g)
	}
	col := f.boardAt("2026-11-05").column("Alfie")
	if col.Goal == nil || !col.Goal.Reached || col.Goal.Title != "Lego" {
		t.Errorf("column goal = %+v, want Lego reached", col.Goal)
	}

	// Claimed: waiting, and the hold doesn't empty the bar.
	red, err := f.svc.ClaimReward(ctx, f.hh, lego.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; !g.Waiting || g.Reached || g.Percent != 100 {
		t.Errorf("after claiming, goal = %+v, want waiting at 100%%", g)
	}
	// Declined: back to reached.
	if err := f.svc.DeclineRedemption(ctx, f.hh, red.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g.Waiting || !g.Reached {
		t.Errorf("after declining, goal = %+v, want reached", g)
	}
	// Approved: the goal is done.
	red, err = f.svc.ClaimReward(ctx, f.hh, lego.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveRedemption(ctx, f.hh, red.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g != nil {
		t.Errorf("after approval, goal = %+v, want none", g)
	}
}

func TestGoalClearedByInstantApproval(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	lego := f.reward("Lego", 10, 0)
	f.earn(f.kid, 10, 0)
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, lego.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClaimReward(ctx, f.hh, lego.ID, f.kid.ID, &f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g != nil {
		t.Errorf("goal = %+v, want cleared", g)
	}
}

func TestSetGoalRefused(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	lego := f.reward("Lego", 10, 0)
	gone := f.reward("Gone", 10, 0)
	if err := f.st.Q.ArchiveReward(ctx, dbq.ArchiveRewardParams{HouseholdID: f.hh, ID: gone.ID, ArchivedAt: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived reward: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing reward: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.SetGoal(ctx, f.hh+1, f.kid.ID, lego.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household: err = %v, want ErrNotFound", err)
	}
	if err := f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{HouseholdID: f.hh, ID: f.kid2.ID, ArchivedAt: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid2.ID, lego.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived person: err = %v, want ErrNotFound", err)
	}

	// Replacing and clearing.
	other := f.reward("Comic", 5, 0)
	for _, id := range []int64{lego.ID, other.ID} {
		if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if g := f.summary(f.kid).Goal; g == nil || g.RewardID != other.ID {
		t.Errorf("goal = %+v, want Comic", g)
	}
	if err := f.svc.ClearGoal(ctx, f.hh, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g != nil {
		t.Errorf("after clearing, goal = %+v", g)
	}
}

// An archived reward never shows as a goal, even if nothing cleared it.
func TestGoalHidesArchivedReward(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	lego := f.reward("Lego", 10, 0)
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, lego.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Q.ArchiveReward(ctx, dbq.ArchiveRewardParams{HouseholdID: f.hh, ID: lego.ID, ArchivedAt: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if g := f.summary(f.kid).Goal; g != nil {
		t.Errorf("goal = %+v, want hidden", g)
	}
	if col := f.boardAt("2026-11-05").column("Alfie"); col.Goal != nil {
		t.Errorf("column goal = %+v, want hidden", col.Goal)
	}
}
