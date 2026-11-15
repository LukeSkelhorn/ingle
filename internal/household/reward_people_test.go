package household

import (
	"errors"
	"slices"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func rewardTitles(rs []dbq.Reward) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func TestRewardPeople(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	alfie := f.add("Alfie", KindChild)
	bea := f.add("Bea", KindChild)

	late, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{
		Title: "Stay up late", CostPoints: 50, Chosen: true, PeopleIDs: []int64{alfie.ID, bea.ID, alfie.ID},
	})
	if err != nil || late.Everyone != 0 {
		t.Fatalf("CreateReward = %+v, %v", late, err)
	}
	if ids, _ := f.svc.RewardPeople(ctx, f.hh, late.ID); !slices.Equal(ids, []int64{alfie.ID, bea.ID}) {
		t.Errorf("people = %v", ids)
	}
	takeaway, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Takeaway", CostPoints: 100})
	if err != nil || takeaway.Everyone != 1 {
		t.Fatalf("everyone reward = %+v, %v", takeaway, err)
	}
	if _, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Sweets", CostPence: 100}); err != nil {
		t.Fatal(err)
	}

	// Who sees what: chosen people, everyone, and no money rewards for
	// adults.
	for _, tc := range []struct {
		who  dbq.Person
		want []string
	}{
		{f.mum, []string{"Takeaway"}},
		{alfie, []string{"Sweets", "Stay up late", "Takeaway"}},
	} {
		list, err := f.svc.RewardsFor(ctx, f.hh, tc.who.ID)
		if err != nil || !slices.Equal(rewardTitles(list), tc.want) {
			t.Errorf("RewardsFor(%s) = %v, %v; want %v", tc.who.Name, rewardTitles(list), err, tc.want)
		}
	}
	all, _ := f.svc.AllRewardPeople(ctx, f.hh)
	if len(all) != 1 || len(all[late.ID]) != 2 {
		t.Errorf("AllRewardPeople = %v", all)
	}

	// Nobody chosen, or an adult on a money reward, is refused.
	for _, in := range []RewardInput{
		{Title: "Nobody", CostPoints: 1, Chosen: true},
		{Title: "Nobody", CostPoints: 1, Chosen: true, PeopleIDs: []int64{9999}},
		{Title: "Mum's sweets", CostPence: 100, Chosen: true, PeopleIDs: []int64{f.mum.ID, alfie.ID}},
	} {
		_, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, in)
		if ve := (valid.Errors{}); !errors.As(err, &ve) || ve["people_ids"] == "" {
			t.Errorf("CreateReward(%+v) = %v, want a people_ids error", in, err)
		}
	}

	// Taking Bea off "Stay up late" clears her goal for it; Alfie keeps his.
	at := int64(1)
	for _, p := range []dbq.Person{alfie, bea} {
		if _, err := f.st.Q.SetGoal(ctx, dbq.SetGoalParams{RewardID: &late.ID, SetAt: &at, HouseholdID: f.hh, ID: p.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.UpdateReward(ctx, f.hh, f.mum.ID, late.ID, RewardInput{
		Title: "Stay up late", CostPoints: 50, Chosen: true, PeopleIDs: []int64{alfie.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.Person(ctx, f.hh, bea.ID); p.GoalRewardID != nil {
		t.Errorf("Bea's goal = %v, want none", *p.GoalRewardID)
	}
	if p, _ := f.svc.Person(ctx, f.hh, alfie.ID); p.GoalRewardID == nil || *p.GoalRewardID != late.ID {
		t.Errorf("Alfie's goal = %v", p.GoalRewardID)
	}

	// Back to everyone: the chosen people go.
	if _, err := f.svc.UpdateReward(ctx, f.hh, f.mum.ID, late.ID, RewardInput{Title: "Stay up late", CostPoints: 50}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := f.svc.RewardPeople(ctx, f.hh, late.ID); len(ids) != 0 {
		t.Errorf("people after everyone = %v", ids)
	}

	// Takeaway starting to cost money clears Mum's goal for it.
	if _, err := f.st.Q.SetGoal(ctx, dbq.SetGoalParams{RewardID: &takeaway.ID, SetAt: &at, HouseholdID: f.hh, ID: f.mum.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateReward(ctx, f.hh, f.mum.ID, takeaway.ID, RewardInput{Title: "Takeaway", CostPoints: 100, CostPence: 500}); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.Person(ctx, f.hh, f.mum.ID); p.GoalRewardID != nil {
		t.Errorf("Mum's goal after it costs money = %v", *p.GoalRewardID)
	}
}
