package server

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestBankBalance(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	alfie := kidWithMoney(t, h, phone, "Alfie", "1357", 800)
	isla := kidWithMoney(t, h, phone, "Isla", "2468", 100)
	page := "/admin/people/" + alfie

	// Not set up: the card asks, and My money says nothing about a bank.
	expect(t, phone.get(page), http.StatusOK, `id="bank-balance"`, "Account name")
	notContains(t, phone.get(page), "Remove bank balance")
	r, tok := openMoney(t, wall, alfie, "1357")
	notContains(t, r, "In my bank")

	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"lots"}}),
		http.StatusUnprocessableEntity, "Enter the balance, like 23.40.", `value="lots"`)
	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {""}, "bank_amount": {"23.40"}}),
		http.StatusUnprocessableEntity, "Name the account")
	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"23.40"}}),
		http.StatusOK, "Updated Alfie&#39;s bank balance.", "Monzo: <strong>£23.40</strong> · updated today", `value="23.40"`, "Remove bank balance")

	// On the wall, in his own My money; not in Isla's, and not on the board.
	expect(t, wall.get("/board/money/"+alfie+"/view?t="+tok), http.StatusOK, "In my bank: <strong>£23.40</strong>", "updated today")
	_, islaTok := openMoney(t, wall, isla, "2468")
	notContains(t, wall.get("/board/money/"+isla+"/view?t="+islaTok), "In my bank")
	notContains(t, wall.get("/"), "23.40")
	expect(t, phone.get(page+"/money"), http.StatusOK, "In my bank: <strong>£23.40</strong>")
	expect(t, phone.get("/admin"), http.StatusOK, "updated Alfie&#39;s bank balance")

	// Adults don't have one.
	expect(t, phone.post("/admin/people/"+itoa(h.person("Mum").ID)+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"1"}}), http.StatusBadRequest)
	notContains(t, phone.get("/admin/people/"+itoa(h.person("Mum").ID)), `id="bank-balance"`)

	// Moving £3.00 adds it on, without counting as a check.
	h.advance(3 * 24 * time.Hour)
	expect(t, phone.post(page+"/bank", url.Values{"amount": {"3"}}), http.StatusOK, "Monzo: <strong>£26.40</strong> · updated 3 days ago")

	// A fortnight on, Admin home nudges; updating clears it.
	notContains(t, phone.get("/admin"), "Check Alfie")
	h.advance(12 * 24 * time.Hour)
	expect(t, phone.get("/admin"), http.StatusOK, "Check Alfie's bank balance", page+"#bank-balance", "updated 15 days ago")
	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"26.40"}}), http.StatusOK)
	notContains(t, phone.get("/admin"), "Check Alfie")

	expect(t, phone.post(page+"/bank-balance/remove", nil), http.StatusOK, "Removed Alfie&#39;s bank balance.")
	_, tok = openMoney(t, wall, alfie, "1357")
	notContains(t, wall.get("/board/money/"+alfie+"/view?t="+tok), "In my bank")
}

func TestBankBalanceModuleOff(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	alfie := kidWithMoney(t, h, phone, "Alfie", "1357", 500)
	page := "/admin/people/" + alfie
	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"1"}}), http.StatusOK)
	h.advance(15 * 24 * time.Hour)
	expect(t, phone.get("/admin"), http.StatusOK, "Check Alfie")

	setModules(t, phone, "rewards", "photos", "calendar")
	notContains(t, phone.get(page), `id="bank-balance"`)
	notContains(t, phone.get("/admin"), "Check Alfie")
	expect(t, phone.post(page+"/bank-balance", url.Values{"bank_label": {"Monzo"}, "bank_amount": {"1"}}), http.StatusNotFound)
	expect(t, phone.post(page+"/bank-balance/remove", nil), http.StatusNotFound)
}
