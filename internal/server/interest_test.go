package server

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestBankOfMumAndDad(t *testing.T) {
	h, _, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	page := "/admin/people/" + itoa(alfie.ID)
	expect(t, phone.get("/admin/settings"), http.StatusOK, "Bank of Mum and Dad", `name="interest_on"`)
	expectNot(t, phone.get(page), "Earns interest")

	on := func(v url.Values) url.Values {
		v.Set("interest_on", "1")
		return v
	}
	expect(t, phone.post("/admin/settings/interest", on(url.Values{"interest_rate": {"lots"}, "interest_money": {"1"}})),
		http.StatusUnprocessableEntity, "Enter a rate like 1 or 0.5.")
	expect(t, phone.post("/admin/settings/interest", on(url.Values{"interest_rate": {"1"}})),
		http.StatusUnprocessableEntity, "Choose money, points or both.")
	expect(t, phone.post("/admin/settings/interest", on(url.Values{"interest_rate": {"11"}, "interest_points": {"1"}})),
		http.StatusUnprocessableEntity, "Enter a rate between 0.01% and 10% a month.")

	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 100, Pence: 1000, Kind: "completion",
		CreatedAt: h.clock().AddDate(0, 0, -30).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	expect(t, phone.post("/admin/settings/interest", on(url.Values{
		"interest_rate": {"10"}, "interest_money": {"1"}, "interest_points": {"1"}, "interest_cap_money": {"0.80"},
	})), http.StatusOK, "Interest saved.", `value="10"`, "That's about 213.8% a year.", `value="0.80"`)

	if n, err := h.srv.Chores.PayInterest(h.ctx(), h.hh); err != nil || n != 0 {
		t.Fatalf("PayInterest in July = %d, %v", n, err)
	}
	since := h.clock().AddDate(0, 0, -20).Unix()
	if _, err := h.st.Q.UpdateInterestSettings(h.ctx(), dbq.UpdateInterestSettingsParams{
		InterestRateBp: 1000, InterestMoney: 1, InterestPoints: 1, InterestCapPence: 80,
		InterestSince: &since, UpdatedAt: since, HouseholdID: h.hh,
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := h.srv.Chores.PayInterest(h.ctx(), h.hh); err != nil || n != 1 {
		t.Fatalf("PayInterest for June = %d, %v", n, err)
	}
	r := phone.get(page + "/money")
	expect(t, r, http.StatusOK, "Interest from the Bank of Mum and Dad", "Interest so far: <strong>£0.80 and 10 pts</strong>",
		"If you keep saving £10.80 and 110 pts, you&#39;ll have about £20.40 and 344 pts this time next year.")
	expect(t, phone.get("/admin"), http.StatusOK, "paid Alfie their monthly interest")

	expect(t, phone.get(page), http.StatusOK, `name="earns_interest" value="1" checked`)
	expect(t, phone.post(page, url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {alfie.Colour}, "avatar_mode": {"none"}, "interest_shown": {"1"},
	}), http.StatusOK, "Saved.")
	if p := h.person("Alfie"); p.InterestOptOut != 1 {
		t.Errorf("opt-out = %d", p.InterestOptOut)
	}
	expectNot(t, phone.get(page+"/money"), "Interest so far")

	expect(t, phone.post("/admin/settings/interest", url.Values{"interest_rate": {"10"}}), http.StatusOK, "Interest is off.")
	expectNot(t, phone.get(page), "Earns interest")
}
