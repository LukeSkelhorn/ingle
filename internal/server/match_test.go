package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestMatchSavings(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	page := "/admin/people/" + itoa(alfie.ID)
	expect(t, phone.get(page), http.StatusOK, "Match savings", "Start matching", `name="match_per_money" value="1.00"`)
	// Adults can't be matched.
	expectNot(t, phone.get("/admin/people/"+itoa(h.person("Mum").ID)), "Match savings")

	expect(t, phone.post(page+"/match", url.Values{"match_add_money": {"0.10"}}),
		http.StatusUnprocessableEntity, "Say how much they need to earn for it.", `value="0.10"`)
	expect(t, phone.post(page+"/match", url.Values{"match_add_points": {"lots"}, "match_per_points": {"10"}}),
		http.StatusUnprocessableEntity, "Enter whole numbers of points.")
	expect(t, phone.post(page+"/match", url.Values{"match_per_money": {"1.00"}, "match_per_points": {"10"}}),
		http.StatusUnprocessableEntity, "Add an amount for money or points, or stop matching.")
	expect(t, phone.post(page+"/match", url.Values{
		"match_add_money": {"0.50"}, "match_per_money": {"1.00"}, "match_per_points": {"10"}, "match_cap_money": {"0.20"},
	}), http.StatusOK, "Matching saved. Alfie will see it on their goal.", "✓ Matching is on.", `value="0.50"`, `value="0.20"`, "Stop matching")
	expect(t, phone.get("/admin"), http.StatusOK, "set up matching for Alfie")

	// Saving for a £5 goal, Alfie sees the offer.
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Lego set"}, "emoji": {"🧱"}, "money": {"5.00"}}), "/admin/rewards")
	var lego int64
	for _, rw := range must(h.st.Q.ListRewards(h.ctx(), h.hh)) {
		if rw.Title == "Lego set" {
			lego = rw.ID
		}
	}
	sheet := "/board/people/" + itoa(alfie.ID)
	r := wall.post(sheet+"/goal", url.Values{"reward": {itoa(lego)}})
	expect(t, r, http.StatusOK, "🤝", "Mum adds £0.50 for every £1.00 you earn (£0.20 left to match).")

	// Hoover the lounge earns 50p; 25p would be matched, but the limit is 20p.
	occ := h.occurrence("Hoover the lounge")
	expect(t, wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", url.Values{"person": {itoa(alfie.ID)}}), http.StatusOK)
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})); b.Pence != 70 {
		t.Errorf("balance = %dp, want 70p", b.Pence)
	}
	r = phone.get(page + "/money")
	expect(t, r, http.StatusOK, "Matched by Mum", "Mum's matching has reached its limit.", "goal__bar-match",
		"including £0.20 matched")
	expect(t, phone.get(page), http.StatusOK, "Matched so far: <strong>£0.20</strong> · the limit is reached")

	// Changing the goal takes it back.
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Comic"}, "money": {"3.00"}}), "/admin/rewards")
	var comic int64
	for _, rw := range must(h.st.Q.ListRewards(h.ctx(), h.hh)) {
		if rw.Title == "Comic" {
			comic = rw.ID
		}
	}
	wall.post(sheet+"/goal", url.Values{"reward": {itoa(comic)}})
	r = phone.get(page + "/money")
	expect(t, r, http.StatusOK, "Match taken back: goal changed", "(£0.20 left to match)")
	if strings.Contains(r.body, "goal__bar-match") {
		t.Error("the new goal shows a matched part")
	}
	expect(t, phone.get("/admin"), http.StatusOK, "took back Alfie&#39;s match after their goal changed")

	// Stopping keeps what was matched.
	expect(t, phone.post(page+"/match/remove", nil), http.StatusOK, "Stopped matching for Alfie.", "Start matching")
	expectNot(t, phone.get(page+"/money"), "🤝 Mum adds")
}
