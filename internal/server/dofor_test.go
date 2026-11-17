package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestDoForChildsChore(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, mum := h.person("Alfie"), h.person("Mum")
	occ := h.occurrence("Feed the cat")
	sheet := "/board/occurrences/" + itoa(occ.ID) + "/do-for?person=" + itoa(alfie.ID)
	post := "/board/occurrences/" + itoa(occ.ID) + "/do-for"
	form := url.Values{"person": {itoa(alfie.ID)}}

	r := wall.get("/board")
	expect(t, r, http.StatusOK, `data-hold="`+strings.ReplaceAll(sheet, "&", "&amp;")+`"`)

	expect(t, wall.get(sheet), http.StatusOK, "data-do-for-pin", "Mum, enter your PIN.", "pinpad__keys",
		`name="by" value="`+itoa(mum.ID)+`" checked`)
	expect(t, wall.post(post, form), http.StatusBadRequest, "Choose who did it.")
	withPIN := func(pin string) url.Values {
		return url.Values{"person": {itoa(alfie.ID)}, "by": {itoa(mum.ID)}, "pin": {pin}}
	}
	expect(t, wall.post(post, withPIN("1111")), http.StatusUnauthorized, "Wrong PIN")
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: mum.ID})); b.Points != 0 {
		t.Errorf("Mum has %d points after a wrong PIN", b.Points)
	}
	notContains(t, wall.get("/board"), "Done by Mum")

	expect(t, phone.get(sheet), http.StatusOK, "Feed the cat", "Alfie", "I did it")
	r = phone.post(post, form)
	expect(t, r, http.StatusOK, `id="board"`, "Done by Mum")
	tt := toastOf(t, r)
	if !strings.Contains(tt.Message, "Done for Alfie") || tt.Undo == "" {
		t.Errorf("toast = %+v, want Done for Alfie with Undo", tt)
	}
	ch := h.chore("Feed the cat")
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: mum.ID})); b.Points != ch.AdultPoints || b.Pence != 0 {
		t.Errorf("Mum's balance = %+v, want %d points", b, ch.AdultPoints)
	}
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})); b.Points != 0 {
		t.Errorf("Alfie's balance = %+v, want nothing", b)
	}
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Done by Mum for Alfie")

	expect(t, phone.post(post, form), http.StatusConflict)

	pool := h.occurrence("Hoover the lounge")
	expect(t, phone.post("/board/occurrences/"+itoa(pool.ID)+"/do-for", url.Values{"person": {itoa(alfie.ID)}}), http.StatusConflict)
	expect(t, phone.get("/board/occurrences/"+itoa(pool.ID)+"/do-for?person="+itoa(mum.ID)), http.StatusConflict)

	r = phone.post(tt.Undo, nil)
	expect(t, r, http.StatusOK)
	if u := toastOf(t, r); u.Message != "Undone." {
		t.Errorf("undo toast = %+v", u)
	}
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: mum.ID})); b.Points != 0 {
		t.Errorf("Mum's balance after undo = %+v", b)
	}
}

func TestDoForWithPIN(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Dad"}, "kind": {"adult"}, "colour": {"#0090FF"}, "avatar_mode": {"none"},
	}))
	alfie, mum, dad := h.person("Alfie"), h.person("Mum"), h.person("Dad")
	occ := h.occurrence("Feed the cat")
	sheet := "/board/occurrences/" + itoa(occ.ID) + "/do-for?person=" + itoa(alfie.ID)
	post := "/board/occurrences/" + itoa(occ.ID) + "/do-for"
	form := func(by int64, pin string) url.Values {
		return url.Values{"person": {itoa(alfie.ID)}, "by": {itoa(by)}, "pin": {pin}}
	}

	r := wall.get(sheet)
	expect(t, r, http.StatusOK, "data-do-for-pin", `name="by" value="`+itoa(mum.ID)+`" checked`)
	expect(t, r, http.StatusOK, `data-step="who" hidden`)
	notContains(t, r, `value="`+itoa(dad.ID)+`"`)

	expect(t, phone.post("/admin/people/"+itoa(dad.ID)+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}), http.StatusOK)
	r = wall.get(sheet)
	expect(t, r, http.StatusOK, "Who did it?", `name="by" value="`+itoa(mum.ID)+`"`, `name="by" value="`+itoa(dad.ID)+`"`)
	notContains(t, r, `" checked>`, `data-step="who" hidden`)

	expect(t, wall.post(post, form(mum.ID, "1357")), http.StatusUnauthorized, "Wrong PIN")
	r = wall.post(post, form(dad.ID, "1357"))
	expect(t, r, http.StatusOK, `id="board"`, "Done by Dad")
	if tt := toastOf(t, r); !strings.HasPrefix(tt.Message, "Thanks, Dad!") || tt.Undo == "" {
		t.Errorf("toast = %+v, want Thanks, Dad! with Undo", tt)
	}
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: dad.ID})); b.Points != h.chore("Feed the cat").AdultPoints {
		t.Errorf("Dad's balance = %+v", b)
	}
	expect(t, wall.post(toastOf(t, r).Undo, nil), http.StatusOK)

	expect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/pin", url.Values{"pin": {"2222"}, "pin_confirm": {"2222"}}), http.StatusOK)
	expect(t, wall.post(post, form(alfie.ID, "2222")), http.StatusUnauthorized, "Wrong PIN")

	for range 5 {
		wall.post(post, form(mum.ID, "0000"))
	}
	expect(t, wall.post(post, form(mum.ID, "2468")), http.StatusTooManyRequests, "Too many tries")
}

func TestDoForNoAdultPIN(t *testing.T) {
	h, wall, _ := boardHousehold(t)
	if _, err := h.st.SQL().ExecContext(h.ctx(), "UPDATE people SET pin_hash = NULL WHERE kind = 'adult'"); err != nil {
		t.Fatal(err)
	}
	occ := h.occurrence("Feed the cat")
	r := wall.get("/board/occurrences/" + itoa(occ.ID) + "/do-for?person=" + itoa(h.person("Alfie").ID))
	expect(t, r, http.StatusOK, "Ask a grown-up to set their PIN")
	notContains(t, r, "pinpad__keys")
}

func TestDoForAnotherAdult(t *testing.T) {
	h, _, phone := boardHousehold(t)
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Dad"}, "kind": {"adult"}, "colour": {"#0090FF"}, "avatar_mode": {"none"},
	}))
	alfie, mum, dad := h.person("Alfie"), h.person("Mum"), h.person("Dad")
	occ := h.occurrence("Feed the cat")
	sheet := "/board/occurrences/" + itoa(occ.ID) + "/do-for?person=" + itoa(alfie.ID)
	post := "/board/occurrences/" + itoa(occ.ID) + "/do-for"

	expect(t, phone.get(sheet), http.StatusOK, "Who did it?",
		`name="by" value="`+itoa(mum.ID)+`" checked`, `name="by" value="`+itoa(dad.ID)+`"`, "Dad")

	expect(t, phone.post(post, url.Values{"person": {itoa(alfie.ID)}, "by": {itoa(h.person("Isla").ID)}}), http.StatusConflict)

	r := phone.post(post, url.Values{"person": {itoa(alfie.ID)}, "by": {itoa(dad.ID)}})
	expect(t, r, http.StatusOK, "Done by Dad")
	if tt := toastOf(t, r); !strings.HasPrefix(tt.Message, "Thanks, Dad!") {
		t.Errorf("toast = %+v, want Thanks, Dad!", tt)
	}
	ch := h.chore("Feed the cat")
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: dad.ID})); b.Points != ch.AdultPoints {
		t.Errorf("Dad's balance = %+v, want %d points", b, ch.AdultPoints)
	}
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: mum.ID})); b.Points != 0 {
		t.Errorf("Mum's balance = %+v, want nothing", b)
	}
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Done by Dad for Alfie")
}
