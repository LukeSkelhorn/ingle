package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Rewards for chosen people: the form saves who, the list says who, and a
// person's sheet, Admin › People's Saving for and the claim only offer the
// rewards they may have.
func TestRewardsForChosenPeople(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	mum, alfie, isla := h.person("Mum"), h.person("Alfie"), h.person("Isla")

	// An adult can't be chosen for a reward that costs money.
	r := phone.post("/admin/rewards", url.Values{
		"title": {"Sweets"}, "cost_points": {"0"}, "money": {"1.00"},
		"reward_who": {"chosen"}, "people_ids": {itoa(mum.ID), itoa(alfie.ID)},
	})
	expect(t, r, http.StatusUnprocessableEntity, "Adults don&#39;t have money")
	r = phone.post("/admin/rewards", url.Values{"title": {"Nobody"}, "cost_points": {"5"}, "reward_who": {"chosen"}})
	expect(t, r, http.StatusUnprocessableEntity, "Choose who it&#39;s for, or Everyone.")

	for _, form := range []url.Values{
		{"title": {"Stay up late"}, "cost_points": {"10"}, "reward_who": {"chosen"}, "people_ids": {itoa(alfie.ID)}},
		{"title": {"Takeaway"}, "cost_points": {"20"}, "reward_who": {"everyone"}},
		{"title": {"Sweets"}, "cost_points": {"0"}, "money": {"1.00"}},
	} {
		expectRedirect(t, phone.post("/admin/rewards", form), "/admin/rewards")
	}
	ids := map[string]int64{}
	for _, rw := range must(h.st.Q.ListRewards(h.ctx(), h.hh)) {
		ids[rw.Title] = rw.ID
	}

	// The form shows who's chosen and marks adults; the list says who.
	expect(t, phone.get("/admin/rewards/"+itoa(ids["Stay up late"])), http.StatusOK,
		`name="reward_who" value="chosen" checked`,
		`value="`+itoa(alfie.ID)+`" data-kind="child" checked`, `value="`+itoa(mum.ID)+`" data-kind="adult">`)
	expect(t, phone.get("/admin/rewards"), http.StatusOK, "For Alfie", "Everyone", "Children")

	// Each sheet lists only that person's rewards.
	for _, tc := range []struct {
		who       string
		id        int64
		has, hasn []string
	}{
		{"Mum", mum.ID, []string{"Takeaway"}, []string{"Stay up late", "Sweets"}},
		{"Alfie", alfie.ID, []string{"Takeaway", "Stay up late", "Sweets"}, nil},
		{"Isla", isla.ID, []string{"Takeaway", "Sweets"}, []string{"Stay up late"}},
	} {
		body := wall.get("/board/people/" + itoa(tc.id)).body
		for _, s := range tc.has {
			if !strings.Contains(body, s) {
				t.Errorf("%s's sheet is missing %s", tc.who, s)
			}
		}
		for _, s := range tc.hasn {
			if strings.Contains(body, s) {
				t.Errorf("%s's sheet shows %s", tc.who, s)
			}
		}
	}

	// Claiming or saving for someone else's reward is refused.
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: isla.ID, Points: 100, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	r = wall.post("/board/rewards/"+itoa(ids["Stay up late"])+"/claim", url.Values{"person": {itoa(isla.ID)}})
	if r.status == http.StatusOK {
		t.Errorf("Isla claimed Alfie's reward: %d", r.status)
	}
	if n := len(must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))); n != 0 {
		t.Errorf("%d claims waiting, want 0", n)
	}
	r = wall.post("/board/people/"+itoa(isla.ID)+"/goal", url.Values{"reward": {itoa(ids["Stay up late"])}})
	if r.status == http.StatusOK && h.person("Isla").GoalRewardID != nil {
		t.Error("Isla saving for Alfie's reward")
	}

	// Admin › People › Saving for lists only theirs.
	page := phone.get("/admin/people/" + itoa(isla.ID))
	if strings.Contains(page.body, "Stay up late") || !strings.Contains(page.body, "Takeaway") {
		t.Error("Isla's Saving for picker isn't filtered")
	}
}
