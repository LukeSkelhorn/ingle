package chores

import (
	"errors"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// chosenReward is a reward for just the given people.
func (f *fixture) chosenReward(title string, points, pence int64, people ...dbq.Person) dbq.Reward {
	f.t.Helper()
	ctx := f.t.Context()
	r, err := f.st.Q.CreateReward(ctx, dbq.CreateRewardParams{HouseholdID: f.hh, Title: title, CostPoints: points, CostPence: pence, CreatedAt: 1})
	if err != nil {
		f.t.Fatal(err)
	}
	if people == nil {
		return r
	}
	if err := f.st.Q.SetRewardEveryone(ctx, dbq.SetRewardEveryoneParams{Everyone: 0, HouseholdID: f.hh, ID: r.ID}); err != nil {
		f.t.Fatal(err)
	}
	for _, p := range people {
		if err := f.st.Q.AddRewardPerson(ctx, dbq.AddRewardPersonParams{HouseholdID: f.hh, RewardID: r.ID, PersonID: p.ID}); err != nil {
			f.t.Fatal(err)
		}
	}
	return r
}

// A reward someone may not have can't be claimed or saved for, even by
// posting its id: it's for other people, or it costs money and they're an
// adult.
func TestRewardForChosenPeople(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for _, p := range []dbq.Person{f.kid, f.kid2, f.adult} {
		if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, p.ID, 100, "test"); err != nil {
			t.Fatal(err)
		}
	}
	late := f.chosenReward("Stay up late", 10, 0, f.kid)
	sweets := f.chosenReward("Sweets", 0, 50)

	for _, tc := range []struct {
		name   string
		reward dbq.Reward
		who    dbq.Person
	}{
		{"not chosen", late, f.kid2},
		{"adult, costs money", sweets, f.adult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.ClaimReward(ctx, f.hh, tc.reward.ID, tc.who.ID, nil); !errors.Is(err, ErrNotFound) {
				t.Errorf("ClaimReward = %v, want ErrNotFound", err)
			}
			if _, err := f.svc.SetGoal(ctx, f.hh, tc.who.ID, tc.reward.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("SetGoal = %v, want ErrNotFound", err)
			}
		})
	}

	// Alfie may: he saves for it and claims it. Then he's taken off it:
	// the waiting claim can still be approved.
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, late.ID); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	red, err := f.svc.ClaimReward(ctx, f.hh, late.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	if err := f.st.Q.DeleteRewardPeople(ctx, dbq.DeleteRewardPeopleParams{HouseholdID: f.hh, RewardID: late.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveRedemption(ctx, f.hh, red.ID, f.adult.ID); err != nil {
		t.Errorf("approve after being taken off: %v", err)
	}
}

// A goal for a reward someone may no longer have doesn't show, even before
// it's cleared.
func TestGoalHiddenWhenNotAllowed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	late := f.chosenReward("Stay up late", 10, 0, f.kid)
	if _, err := f.svc.SetGoal(ctx, f.hh, f.kid.ID, late.ID); err != nil {
		t.Fatal(err)
	}
	weekStart := f.svc.now().AddDate(0, 0, -1)
	if sum, _ := f.svc.PersonSummary(ctx, f.hh, f.kid.ID, weekStart); sum.Goal == nil {
		t.Fatal("no goal while allowed")
	}
	if err := f.st.Q.DeleteRewardPeople(ctx, dbq.DeleteRewardPeopleParams{HouseholdID: f.hh, RewardID: late.ID}); err != nil {
		t.Fatal(err)
	}
	if sum, _ := f.svc.PersonSummary(ctx, f.hh, f.kid.ID, weekStart); sum.Goal != nil {
		t.Errorf("goal = %+v, want none", sum.Goal)
	}
}
