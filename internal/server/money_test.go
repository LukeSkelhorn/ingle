package server

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
)

var moneyToken = regexp.MustCompile(`/view\?t=([A-Za-z0-9_-]+)`)

// addKid adds a child from the phone and returns their id.
func addKid(t *testing.T, h *harness, phone *device, name string) string {
	t.Helper()
	r := phone.post("/admin/people", url.Values{
		"name": {name}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"},
	})
	id := itoa(h.person(name).ID)
	expectRedirect(t, r, "/admin/people/"+id)
	return id
}

func TestKidMoneyPIN(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp() // a display, unlocked as Mum
	phone := h.phone()
	alfie, isla := addKid(t, h, phone, "Alfie"), addKid(t, h, phone, "Isla")
	expect(t, phone.post("/admin/people/"+alfie+"/points", url.Values{
		"direction": {"give"}, "amount": {"5"}, "reason": {"Tidied up"},
	}), http.StatusOK)

	// No PIN yet: ask a grown-up.
	expect(t, wall.get("/board/people/"+alfie), http.StatusOK, `hx-get="/board/money/`+alfie+`"`, "My money")
	r := wall.get("/board/money/" + alfie)
	expect(t, r, http.StatusOK, "Alfie's money", "Ask a grown-up to set your PIN")
	notContains(t, r, "pinpad__keys")

	// A grown-up sets it in Admin › People.
	expect(t, phone.post("/admin/people/"+alfie+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}), http.StatusOK, "PIN saved.", "A PIN is set.")
	expect(t, wall.get("/board/money/"+alfie), http.StatusOK, "Enter your PIN", `data-pinpad data-length="4"`)
	expect(t, wall.get("/"), http.StatusOK, "/static/pinpad.js")

	// The wrong PIN, then the right one.
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"0000"}}), http.StatusUnauthorized, "Wrong PIN")
	r = wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}})
	expect(t, r, http.StatusOK, "Alfie's money", "5 pts")
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", r.header.Get("Cache-Control"))
	}
	m := moneyToken.FindStringSubmatch(r.body)
	if m == nil {
		t.Fatalf("no refresh token in\n%s", r.body)
	}
	tok := m[1]
	expect(t, wall.get("/board/money/"+alfie+"/view?t="+tok), http.StatusOK, "5 pts", "/view?t=")
	// It's Alfie's only, on this display only, for a minute.
	expect(t, wall.get("/board/money/"+isla+"/view?t="+tok), http.StatusOK, "Isla's money", "Ask a grown-up")
	notContains(t, phone.get("/board/money/"+alfie+"/view?t="+tok), "5 pts")
	h.advance(61e9)
	r = wall.get("/board/money/" + alfie + "/view?t=" + tok)
	expect(t, r, http.StatusOK, "Enter your PIN again")
	notContains(t, r, "5 pts")

	// Alfie's PIN never opens admin or the displays.
	expectRedirect(t, wall.post("/admin/lock", nil), "/")
	expect(t, wall.post("/admin/unlock", url.Values{"person": {alfie}, "pin": {"1357"}}), http.StatusUnauthorized)
	expectRedirect(t, wall.get("/admin"), "/admin/unlock")
	notContains(t, wall.get("/admin/unlock"), "Alfie")
	expectRedirect(t, phone.post("/lock", nil), "/")
	expect(t, wall.post("/unlock", url.Values{"code": {"1357"}}), http.StatusUnauthorized)
	expect(t, wall.post("/unlock", url.Values{"person": {alfie}, "code": {"1357"}}), http.StatusUnauthorized)
	// While the displays are locked, My money doesn't open.
	for _, p := range []string{"/board/money/" + alfie, "/board/money/" + alfie + "/view"} {
		if r := wall.get(p); r.status != http.StatusLocked {
			t.Errorf("GET %s while locked = %d", p, r.status)
		}
	}
	if r := wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}); r.status != http.StatusLocked {
		t.Errorf("POST pin while locked = %d", r.status)
	}
	expectRedirect(t, phone.post("/unlock", nil), "/")

	// Mum's PIN opens her own money, and still unlocks admin.
	mum := itoa(h.person("Mum").ID)
	expect(t, wall.post("/board/money/"+mum+"/pin", url.Values{"pin": {"2468"}}), http.StatusOK, "Mum's money")
	expectRedirect(t, wall.post("/admin/unlock", url.Values{"person": {mum}, "pin": {"2468"}}), "/admin")

	// My money is for household displays.
	expect(t, phone.get("/board/money/"+alfie), http.StatusOK, "opens on a household display")
	expect(t, phone.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}), http.StatusForbidden)
	notContains(t, phone.get("/board/people/"+alfie), "/board/money/")
}

func TestKidMoneyPINLockout(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	alfie := addKid(t, h, phone, "Alfie")
	expect(t, phone.post("/admin/people/"+alfie+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}), http.StatusOK)
	for range 4 {
		expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"0000"}}), http.StatusUnauthorized, "Wrong PIN")
	}
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"0000"}}), http.StatusTooManyRequests, "Try again in 5 minutes")
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}), http.StatusTooManyRequests)
	h.advance(5 * 60e9)
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}), http.StatusOK, "Alfie's money")
}

func TestMoneyModuleOff(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	alfie := addKid(t, h, phone, "Alfie")
	expect(t, phone.post("/admin/people/"+alfie+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}), http.StatusOK)

	setModules(t, phone, "rewards", "photos", "calendar")
	for _, p := range []string{"/board/money/" + alfie, "/board/money/" + alfie + "/view"} {
		expect(t, wall.get(p), http.StatusNotFound)
	}
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}), http.StatusNotFound)
	notContains(t, wall.get("/board/people/"+alfie), "/board/money/")
	notContains(t, wall.get("/"), "/static/pinpad.js")
	// No kid PIN card, and no new kid PINs; adults keep theirs.
	notContains(t, phone.get("/admin/people/"+alfie), `/pin"`)
	expect(t, phone.post("/admin/people/"+alfie+"/pin", url.Values{"pin": {"2222"}, "pin_confirm": {"2222"}}), http.StatusConflict, "Turn My money on")
	expect(t, phone.get("/admin/people/"+itoa(h.person("Mum").ID)), http.StatusOK, "Unlocks admin on a household display. A PIN is set.")

	// Back on, Alfie's PIN still works.
	setModules(t, phone, "rewards", "money", "photos", "calendar")
	expect(t, wall.post("/board/money/"+alfie+"/pin", url.Values{"pin": {"1357"}}), http.StatusOK, "Alfie's money")
}
