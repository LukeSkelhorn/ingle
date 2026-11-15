package chores

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// doChore has p complete a new pool chore worth points and pence, today.
func (f *fixture) doChore(p dbq.Person, points, pence int64, approval bool) dbq.Completion {
	f.t.Helper()
	f.chores++
	ch := f.chore(fmt.Sprintf("Chore %d", f.chores), choreOpts{points: points, pence: pence, approval: approval})
	if _, err := f.svc.Materialise(f.t.Context(), f.hh, f.svc.now(), 1); err != nil {
		f.t.Fatal(err)
	}
	for _, r := range f.board("2026-11-02", "2026-11-02") {
		if r.ChoreID == ch.ID {
			c, err := f.svc.Complete(f.t.Context(), f.hh, r.ID, p.ID)
			if err != nil {
				f.t.Fatalf("Complete: %v", err)
			}
			return c
		}
	}
	f.t.Fatalf("no occurrence for %s", ch.Title)
	return dbq.Completion{}
}

func (f *fixture) offer(p dbq.Person, in MatchInput) {
	f.t.Helper()
	if err := f.svc.SaveMatchOffer(f.t.Context(), f.hh, p.ID, f.adult.ID, in); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) goal(p dbq.Person, r dbq.Reward) {
	f.t.Helper()
	if _, err := f.svc.SetGoal(f.t.Context(), f.hh, p.ID, r.ID); err != nil {
		f.t.Fatal(err)
	}
}

// matched is what p holds from matching, and the match ledger lines.
func (f *fixture) matched(p dbq.Person) (points, pence int64, lines []dbq.LedgerEntry) {
	f.t.Helper()
	held, err := f.st.Q.MatchHeldForPerson(f.t.Context(), dbq.MatchHeldForPersonParams{HouseholdID: f.hh, PersonID: p.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.st.SQL().QueryContext(f.t.Context(),
		"SELECT points, pence, note, created_by FROM ledger_entries WHERE person_id = ? AND kind = 'match' ORDER BY id", p.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e dbq.LedgerEntry
		if err := rows.Scan(&e.Points, &e.Pence, &e.Note, &e.CreatedBy); err != nil {
			f.t.Fatal(err)
		}
		lines = append(lines, e)
	}
	return held.Points, held.Pence, lines
}

func (f *fixture) wantMatchVerbs(want ...string) {
	f.t.Helper()
	var got []string
	for _, v := range slices.Backward(f.verbs()) {
		if strings.HasPrefix(v, "match.") {
			got = append(got, v)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Errorf("match activity = %v, want %v", got, want)
	}
}

// tenPence is 10p for every £1 and 1 point for every 10.
var tenPence = MatchInput{AddPence: 10, PerPence: 100, AddPoints: 1, PerPoints: 10}

func TestMatchProportionalRounded(t *testing.T) {
	f := newFixture(t)
	f.goal(f.kid, f.reward("Lego", 1000, 5000))
	f.offer(f.kid, tenPence)

	f.doChore(f.kid, 15, 50, false) // 5p, 1 pt (1.5 rounded down)
	f.doChore(f.kid, 5, 5, false)   // 0p and 0 pts: nothing
	f.doChore(f.kid, 0, 100, false) // 10p
	pts, pence, lines := f.matched(f.kid)
	if pts != 1 || pence != 15 || len(lines) != 2 {
		t.Fatalf("matched %d pts %dp in %+v; want 1 pt 15p in 2 lines", pts, pence, lines)
	}
	if lines[0].Note != "Matched by Mum" || lines[0].CreatedBy == nil || *lines[0].CreatedBy != f.adult.ID {
		t.Errorf("line = %+v, want Matched by Mum", lines[0])
	}
	if b := f.balance(f.kid); b.Points != 21 || b.Pence != 170 {
		t.Errorf("balance = %+v, want 21 pts 170p", b)
	}
	// It isn't "earned this week": that's chores.
	if s := f.summary(f.kid); s.WeekPoints != 20 || s.WeekPence != 155 {
		t.Errorf("this week = %d pts %dp, want 20 pts 155p", s.WeekPoints, s.WeekPence)
	}
	// The goal shows the matched part and the offer.
	g := f.summary(f.kid).Goal
	if g == nil || g.MatchedPence != 15 || g.MatchedPoints != 1 || g.Offer == nil || g.Offer.SetByName != "Mum" {
		t.Fatalf("goal = %+v", g)
	}
	// 170 of 5000p is 3%; 21 of 1000 pts is 2%, the slower: 1 of 21 matched.
	if g.Percent != 2 || g.MatchPercent != 0 {
		t.Errorf("bar = %d%% with %d%% matched", g.Percent, g.MatchPercent)
	}
}

func TestMatchOnlyWithGoalAndOffer(t *testing.T) {
	f := newFixture(t)
	lego := f.reward("Lego", 100, 1000)

	f.offer(f.kid, tenPence)
	f.doChore(f.kid, 10, 100, false) // no goal yet
	f.goal(f.kid2, lego)
	f.doChore(f.kid2, 10, 100, false) // goal, but no offer
	if _, p, _ := f.matched(f.kid); p != 0 {
		t.Errorf("Alfie matched %dp without a goal", p)
	}
	if _, p, _ := f.matched(f.kid2); p != 0 {
		t.Errorf("Isla matched %dp without an offer", p)
	}
	// Adults can't be offered a match.
	if err := f.svc.SaveMatchOffer(t.Context(), f.hh, f.adult.ID, f.adult.ID, tenPence); !errors.Is(err, ErrNotFound) {
		t.Errorf("offer for an adult = %v", err)
	}
	// Only adults set one up.
	if err := f.svc.SaveMatchOffer(t.Context(), f.hh, f.kid.ID, f.kid2.ID, tenPence); !errors.Is(err, ErrNotAdult) {
		t.Errorf("offer by a child = %v", err)
	}
	// Turned off, nothing more is matched, and nothing is taken back.
	f.goal(f.kid, lego)
	f.doChore(f.kid, 10, 100, false)
	if err := f.svc.RemoveMatchOffer(t.Context(), f.hh, f.kid.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	f.doChore(f.kid, 10, 100, false)
	if pts, p, _ := f.matched(f.kid); pts != 1 || p != 10 {
		t.Errorf("matched %d pts %dp, want 1 pt 10p", pts, p)
	}
	f.wantMatchVerbs("match.set", "match.off")
}

func TestMatchModules(t *testing.T) {
	f := newFixture(t)
	on := map[string]bool{moduleRewards: true, moduleMoney: false}
	f.svc.SetModules(func(_ context.Context, _ *dbq.Queries, _ int64, id string) bool { return on[id] })
	f.goal(f.kid, f.reward("Lego", 100, 1000))
	f.offer(f.kid, tenPence)
	f.doChore(f.kid, 10, 100, false)
	if pts, p, _ := f.matched(f.kid); pts != 1 || p != 0 {
		t.Errorf("money off: matched %d pts %dp, want points only", pts, p)
	}
	if g := f.summary(f.kid).Goal; g.Offer == nil || g.Offer.Money() {
		t.Errorf("money off: offer = %+v, want points only", g.Offer)
	}
	on[moduleRewards] = false
	f.doChore(f.kid, 10, 100, false)
	if pts, _, _ := f.matched(f.kid); pts != 1 {
		t.Errorf("rewards off: matched %d pts, want no more", pts)
	}
}

func TestMatchStopsAtGoal(t *testing.T) {
	f := newFixture(t)
	f.goal(f.kid, f.reward("Comic", 0, 300))
	f.offer(f.kid, MatchInput{AddPence: 50, PerPence: 100})
	f.doChore(f.kid, 0, 200, false) // 100p matched: 300 of 300
	f.doChore(f.kid, 0, 100, false) // past the goal: nothing
	if _, p, lines := f.matched(f.kid); p != 100 || len(lines) != 1 {
		t.Fatalf("matched %dp in %d lines, want 100p in 1", p, len(lines))
	}

	// Not past it either: 250 earned and 125 matched would be 375.
	f2 := newFixture(t)
	f2.goal(f2.kid, f2.reward("Comic", 0, 300))
	f2.offer(f2.kid, MatchInput{AddPence: 50, PerPence: 100})
	f2.doChore(f2.kid, 0, 250, false)
	if _, p, _ := f2.matched(f2.kid); p != 50 {
		t.Errorf("matched %dp, want 50p (just enough)", p)
	}
	if g := f2.summary(f2.kid).Goal; !g.Reached || g.MatchedPence != 50 || g.MatchPercent != 16 || g.OwnPercent() != 84 {
		t.Errorf("goal = %+v", g)
	}

	// Not while a claim is waiting.
	f3 := newFixture(t)
	comic := f3.reward("Comic", 0, 300)
	f3.goal(f3.kid, comic)
	f3.earn(f3.kid, 0, 300)
	if _, err := f3.svc.ClaimReward(t.Context(), f3.hh, comic.ID, f3.kid.ID, nil); err != nil {
		t.Fatal(err)
	}
	f3.offer(f3.kid, MatchInput{AddPence: 50, PerPence: 100})
	f3.doChore(f3.kid, 0, 100, false)
	if _, p, _ := f3.matched(f3.kid); p != 0 {
		t.Errorf("matched %dp while waiting", p)
	}
}

func TestMatchCap(t *testing.T) {
	f := newFixture(t)
	lego := f.reward("Lego", 0, 10000)
	comic := f.reward("Comic", 0, 10000)
	f.goal(f.kid, lego)
	f.offer(f.kid, MatchInput{AddPence: 10, PerPence: 100, CapPence: 25})
	f.doChore(f.kid, 0, 100, false)
	f.doChore(f.kid, 0, 100, false)
	f.doChore(f.kid, 0, 100, false) // 10 + 10 + 5 (the cap)
	f.doChore(f.kid, 0, 100, false) // nothing
	if _, p, _ := f.matched(f.kid); p != 25 {
		t.Fatalf("matched %dp, want the cap, 25p", p)
	}
	if o := f.summary(f.kid).Goal.Offer; o == nil || !o.Spent() || o.CapLeftPence() != 0 {
		t.Errorf("offer = %+v, want spent", o)
	}
	// Changing the goal takes it back, which frees the cap.
	f.goal(f.kid, comic)
	if _, p, _ := f.matched(f.kid); p != 0 {
		t.Fatalf("after taking back, holds %dp", p)
	}
	f.doChore(f.kid, 0, 100, false)
	if _, p, _ := f.matched(f.kid); p != 10 {
		t.Errorf("matched %dp, want 10p again", p)
	}
}

func TestMatchOnApproval(t *testing.T) {
	f := newFixture(t)
	f.goal(f.kid, f.reward("Lego", 100, 0))
	f.offer(f.kid, tenPence)
	c := f.doChore(f.kid, 20, 0, true)
	if pts, _, _ := f.matched(f.kid); pts != 0 {
		t.Fatalf("matched %d pts before approval", pts)
	}
	if err := f.svc.Approve(t.Context(), f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if pts, _, _ := f.matched(f.kid); pts != 2 {
		t.Errorf("matched %d pts after approval, want 2", pts)
	}
}

func TestMatchUndoAndReverse(t *testing.T) {
	f := newFixture(t)
	f.goal(f.kid, f.reward("Lego", 0, 10000))
	f.offer(f.kid, MatchInput{AddPence: 50, PerPence: 100})

	// Undo removes the match with the chore.
	c := f.doChore(f.kid, 0, 100, false)
	if err := f.svc.Undo(t.Context(), f.hh, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, p, lines := f.matched(f.kid); p != 0 || len(lines) != 0 {
		t.Fatalf("after undo: %dp in %+v", p, lines)
	}

	// A reversal takes it back, as much as is left.
	c = f.doChore(f.kid, 0, 200, false) // +100p matched: 300p
	f.earn(f.kid, 0, -250)              // spent: 50p left
	base := f.svc.now()
	f.clock(base.Add(time.Hour))
	if err := f.svc.Reverse(t.Context(), f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	// The reversal took 200p (to −150p); nothing's left to take the match
	// from, so it stays.
	if _, p, _ := f.matched(f.kid); p != 100 {
		t.Errorf("after reversal while overdrawn: holds %dp, want 100p", p)
	}

	f2 := newFixture(t)
	f2.goal(f2.kid, f2.reward("Lego", 0, 10000))
	f2.offer(f2.kid, MatchInput{AddPence: 50, PerPence: 100})
	f2.earn(f2.kid, 0, 30)
	c = f2.doChore(f2.kid, 0, 200, false) // 330p with 100p matched
	f2.clock(f2.svc.now().Add(time.Hour))
	if err := f2.svc.Reverse(t.Context(), f2.hh, c.ID, f2.adult.ID); err != nil {
		t.Fatal(err)
	}
	// 130p left after the reversal; all 100p of the match goes.
	_, p, lines := f2.matched(f2.kid)
	if p != 0 || len(lines) != 2 || lines[1].Note != "Match taken back: Chore 1" {
		t.Errorf("after reversal: holds %dp in %+v", p, lines)
	}
	if b := f2.balance(f2.kid); b.Pence != 30 {
		t.Errorf("balance = %dp, want 30p", b.Pence)
	}
}

func TestMatchTakenBackWhenGoalChanges(t *testing.T) {
	f := newFixture(t)
	lego := f.reward("Lego", 0, 10000)
	comic := f.reward("Comic", 0, 10000)
	f.goal(f.kid, lego)
	f.offer(f.kid, MatchInput{AddPence: 50, PerPence: 100})
	f.doChore(f.kid, 0, 200, false) // 100p matched
	// Picking the same goal again keeps it.
	f.goal(f.kid, lego)
	if g := f.summary(f.kid).Goal; g.MatchedPence != 100 {
		t.Fatalf("same goal again: matched %dp, want 100p", g.MatchedPence)
	}
	// Spent some: only what's left can be taken back.
	f.earn(f.kid, 0, -250) // 50p left
	f.goal(f.kid, comic)
	if _, p, lines := f.matched(f.kid); p != 50 || len(lines) != 2 || lines[1].Pence != -50 || lines[1].Note != matchTakenBackGoal {
		t.Fatalf("after changing goal: holds %dp in %+v", p, lines)
	}
	if b := f.balance(f.kid); b.Pence != 0 {
		t.Errorf("balance = %dp, want 0", b.Pence)
	}
	// The new goal's match is its own; removing the goal takes it back.
	f.doChore(f.kid, 0, 100, false)
	if err := f.svc.ClearGoal(t.Context(), f.hh, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if _, p, _ := f.matched(f.kid); p != 50 {
		t.Errorf("after clearing: holds %dp, want 50p (the old goal's, spent)", p)
	}
	if b := f.balance(f.kid); b.Pence != 100 {
		t.Errorf("balance = %dp, want 100p", b.Pence)
	}
	f.wantMatchVerbs("match.set", "match.taken_back", "match.taken_back")
}

func TestMatchKeptWhenClaimedOrArchived(t *testing.T) {
	f := newFixture(t)
	comic := f.reward("Comic", 0, 300)
	f.goal(f.kid, comic)
	f.offer(f.kid, MatchInput{AddPence: 50, PerPence: 100})
	f.doChore(f.kid, 0, 200, false) // 300p: enough
	d, err := f.svc.ClaimReward(t.Context(), f.hh, comic.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveRedemption(t.Context(), f.hh, d.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if _, p, lines := f.matched(f.kid); p != 100 || len(lines) != 1 {
		t.Errorf("after claiming: %dp in %+v, want the match kept", p, lines)
	}

	// An archived reward isn't the child's doing: they keep the match.
	lego := f.reward("Lego", 0, 10000)
	f.goal(f.kid2, lego)
	f.offer(f.kid2, MatchInput{AddPence: 50, PerPence: 100})
	f.doChore(f.kid2, 0, 200, false)
	// As household.ArchiveReward does.
	if err := f.st.Q.ClearGoalsForReward(t.Context(), dbq.ClearGoalsForRewardParams{HouseholdID: f.hh, GoalRewardID: &lego.ID}); err != nil {
		t.Fatal(err)
	}
	if _, p, _ := f.matched(f.kid2); p != 100 {
		t.Errorf("after archiving: holds %dp, want 100p", p)
	}
}

func TestMatchNotForOtherMoney(t *testing.T) {
	f := newFixture(t)
	f.goal(f.kid, f.reward("Lego", 1000, 10000))
	f.offer(f.kid, tenPence)
	if _, err := f.svc.AdjustPoints(t.Context(), f.hh, f.adult.ID, f.kid.ID, 50, "Bonus"); err != nil {
		t.Fatal(err)
	}
	f.earnAt(f.kid, 0, 500, f.svc.now()) // like interest or a transfer: not a chore credit
	if pts, p, _ := f.matched(f.kid); pts != 0 || p != 0 {
		t.Errorf("matched %d pts %dp from non-chore money", pts, p)
	}
	// Adults doing a chore aren't matched (they have no offer anyway).
	f.doChore(f.adult, 10, 0, false)
	if pts, _, _ := f.matched(f.adult); pts != 0 {
		t.Errorf("adult matched %d pts", pts)
	}
}

func TestSaveMatchOfferValidates(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		in    MatchInput
		field string
	}{
		{MatchInput{}, "match_money"},
		{MatchInput{AddPence: 10}, "match_money"},
		{MatchInput{AddPoints: 1}, "match_points"},
		{MatchInput{AddPence: -1, PerPence: 100}, "match_money"},
		{MatchInput{AddPence: 10, PerPence: 100, CapPence: -1}, "match_cap_money"},
	} {
		err := f.svc.SaveMatchOffer(t.Context(), f.hh, f.kid.ID, f.adult.ID, c.in)
		var errs valid.Errors
		if !errors.As(err, &errs) || errs[c.field] == "" {
			t.Errorf("%+v: err = %v, want one on %s", c.in, err, c.field)
		}
	}
	// A half left blank is off.
	f.offer(f.kid, MatchInput{AddPoints: 1, PerPoints: 10, PerPence: 100})
	o, err := f.svc.MatchOffer(t.Context(), f.hh, f.kid.ID)
	if err != nil || o == nil || o.Money() || !o.Points() || o.PerPence != 0 || o.Capped() {
		t.Errorf("offer = %+v, %v", o, err)
	}
}

func TestMatchAmount(t *testing.T) {
	for _, c := range []struct{ earned, add, per, left, want int64 }{
		{100, 10, 100, -1, 10},
		{50, 10, 100, -1, 5},
		{5, 10, 100, -1, 0},
		{1000, 10, 100, 30, 30},
		{100, 0, 100, -1, 0},
		{-100, 10, 100, -1, 0},
	} {
		if got := matchAmount(c.earned, c.add, c.per, c.left); got != c.want {
			t.Errorf("matchAmount(%d, %d, %d, %d) = %d, want %d", c.earned, c.add, c.per, c.left, got, c.want)
		}
	}
}
