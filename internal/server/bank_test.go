package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// kidWithMoney adds a child with a PIN and pence to spend, and returns
// their id.
func kidWithMoney(t *testing.T, h *harness, phone *device, name, pin string, pence int64) string {
	t.Helper()
	id := addKid(t, h, phone, name)
	expect(t, phone.post("/admin/people/"+id+"/pin", url.Values{"pin": {pin}, "pin_confirm": {pin}}), http.StatusOK)
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: h.person(name).ID, Pence: pence, Kind: "manual", Note: "Pocket money",
		CreatedAt: h.clock().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// openMoney types pin on the wall and returns My money and its token.
func openMoney(t *testing.T, wall *device, id, pin string) (resp, string) {
	t.Helper()
	r := wall.post("/board/money/"+id+"/pin", url.Values{"pin": {pin}})
	expect(t, r, http.StatusOK)
	m := moneyToken.FindStringSubmatch(r.body)
	if m == nil {
		t.Fatalf("no token in\n%s", r.body)
	}
	return r, m[1]
}

// pendingTransfer is the id of name's waiting bank request.
func pendingTransfer(t *testing.T, h *harness, name string) string {
	t.Helper()
	rows, err := h.st.Q.PendingTransfersForPerson(t.Context(), dbq.PendingTransfersForPersonParams{HouseholdID: h.hh, PersonID: h.person(name).ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending transfers = %+v, %v", rows, err)
	}
	return itoa(rows[0].ID)
}

func TestBankRequest(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	alfie := kidWithMoney(t, h, phone, "Alfie", "1357", 800)
	isla := kidWithMoney(t, h, phone, "Isla", "2468", 100)
	base := "/board/money/" + alfie

	r, tok := openMoney(t, wall, alfie, "1357")
	expect(t, r, http.StatusOK, "Ask to move to my bank", base+"/bank?t="+tok)

	// The ask panel starts at £1.00 and goes up to all he has.
	expect(t, wall.get(base+"/bank?t="+tok), http.StatusOK, "data-bank-stepper", `data-max="800"`, `data-step="50"`, `name="pence" value="100"`, "All of it (£8.00)")
	// Without his PIN check, back to the keypad.
	r = wall.get(base + "/bank")
	expect(t, r, http.StatusOK, "Enter your PIN again")
	if r.header.Get("HX-Retarget") != "#sheet-body" {
		t.Errorf("HX-Retarget = %q", r.header.Get("HX-Retarget"))
	}
	_, islaTok := openMoney(t, wall, isla, "2468")
	expect(t, wall.post(base+"/bank", url.Values{"t": {islaTok}, "pence": {"100"}}), http.StatusOK, "Enter your PIN again")
	// Only 50p steps.
	expect(t, wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"125"}}), http.StatusBadRequest)

	// He asks for £2.50: it's held, and waits.
	r = wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"250"}})
	expect(t, r, http.StatusOK, "£2.50</strong> to your bank", "Waiting for a grown-up", "(£2.50 held)", "/cancel")
	notContains(t, r, "Ask to move to my bank")
	if !strings.Contains(r.header.Get("HX-Trigger"), "A grown-up will move it") {
		t.Errorf("HX-Trigger = %q", r.header.Get("HX-Trigger"))
	}
	r = wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"100"}})
	if !strings.Contains(r.header.Get("HX-Trigger"), "already asked") {
		t.Errorf("second request HX-Trigger = %q", r.header.Get("HX-Trigger"))
	}
	// Adults pay out from Admin › People instead, and can't cancel for him.
	expect(t, phone.post(base+"/bank", url.Values{"pence": {"100"}}), http.StatusForbidden)
	r = phone.get(base + "/view")
	expect(t, r, http.StatusOK, "Waiting for a grown-up")
	notContains(t, r, "/cancel")

	// A grown-up sees it on Admin home and in Admin › Bank, and declines.
	expect(t, phone.get("/admin"), http.StatusOK, `href="/admin/bank"`, "Bank request<", "asked to move £2.50 to their bank")
	req := pendingTransfer(t, h, "Alfie")
	expect(t, phone.get("/admin/bank"), http.StatusOK, "£2.50 to Alfie", "/admin/bank/"+req+"/approve", "/admin/bank/"+req+"/decline")
	expect(t, phone.post("/admin/bank/"+req+"/decline", url.Values{"reason": {" "}}), http.StatusUnprocessableEntity, "Say why")
	expect(t, phone.post("/admin/bank/"+req+"/decline", url.Values{"reason": {"Let's talk about it"}}), http.StatusOK, "Declined.", "Declined by Mum")
	r = wall.get(base + "/view?t=" + tok)
	expect(t, r, http.StatusOK, "£2.50 wasn", "talk about it", "Ask to move to my bank")
	notContains(t, r, "held)")
	expect(t, phone.post("/admin/bank/"+req+"/approve", nil), http.StatusConflict, "already been decided")

	// He asks again, then changes his mind.
	expect(t, wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"500"}}), http.StatusOK, "Waiting for a grown-up")
	req = pendingTransfer(t, h, "Alfie")
	expect(t, wall.post(base+"/bank/"+req+"/cancel", url.Values{"t": {islaTok}}), http.StatusOK, "Enter your PIN again")
	r = wall.post(base+"/bank/"+req+"/cancel", url.Values{"t": {tok}})
	expect(t, r, http.StatusOK, "Ask to move to my bank")
	notContains(t, r, "Waiting for a grown-up")

	// And again; this time Mum sends it.
	expect(t, wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"300"}}), http.StatusOK)
	req = pendingTransfer(t, h, "Alfie")
	expect(t, phone.post("/admin/bank/"+req+"/approve", nil), http.StatusOK, "Approved.", "Sent by Mum")
	expect(t, wall.get(base+"/view?t="+tok), http.StatusOK, "£3.00 moved to your bank", "£5.00")
	expect(t, phone.get("/admin"), http.StatusOK, "approved moving £3.00 to Alfie")
	notContains(t, phone.get("/admin"), "Bank request")

	// While the displays are locked, nothing.
	expectRedirect(t, phone.post("/lock", nil), "/")
	if r := wall.post(base+"/bank", url.Values{"t": {tok}, "pence": {"100"}}); r.status != http.StatusLocked {
		t.Errorf("POST bank while locked = %d", r.status)
	}
}

func TestPayOut(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	alfie := kidWithMoney(t, h, phone, "Alfie", "1357", 500)
	page := "/admin/people/" + alfie

	expect(t, phone.get(page), http.StatusOK, "Move to bank", "<strong>£5.00</strong> to spend", "All of it (£5.00)")
	expect(t, phone.post(page+"/bank", url.Values{"amount": {"9"}}), http.StatusUnprocessableEntity, "more than Alfie has to spend")
	expect(t, phone.post(page+"/bank", url.Values{"amount": {"lots"}}), http.StatusUnprocessableEntity, "Enter an amount like 5.00.")
	expect(t, phone.post(page+"/bank", url.Values{"amount": {"1.20"}}), http.StatusOK, "Moved £1.20 to Alfie", "<strong>£3.80</strong> to spend")
	expect(t, phone.post(page+"/bank", url.Values{"all": {"1"}}), http.StatusOK, "Moved £3.80 to Alfie")
	r := phone.get(page)
	expect(t, r, http.StatusOK, "<strong>£0.00</strong> to spend")
	notContains(t, r, "Record the move")
	expect(t, phone.get("/admin/bank"), http.StatusOK, "£3.80 to Alfie", "Moved by Mum")
	expect(t, phone.get(page+"/money"), http.StatusOK, "£1.20 moved to your bank", "£3.80 moved to your bank")
	expect(t, phone.get("/admin"), http.StatusOK, "moved £3.80 to Alfie")
}

func TestBankModuleOff(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	alfie := kidWithMoney(t, h, phone, "Alfie", "1357", 500)
	setModules(t, phone, "rewards", "photos", "calendar")

	expect(t, phone.get("/admin/bank"), http.StatusNotFound)
	notContains(t, phone.get("/admin"), `href="/admin/bank"`)
	notContains(t, phone.get("/admin/people/"+alfie), "Move to bank")
	expect(t, phone.post("/admin/people/"+alfie+"/bank", url.Values{"all": {"1"}}), http.StatusNotFound)
	expect(t, wall.get("/board/money/"+alfie+"/bank"), http.StatusNotFound)
	expect(t, wall.post("/board/money/"+alfie+"/bank", url.Values{"pence": {"100"}}), http.StatusNotFound)
}
