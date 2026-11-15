package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// The Up for grabs picker shows what each person would earn: adults get
// the points only.
func TestPickerShowsWhatEachPersonEarns(t *testing.T) {
	h, wall, _ := boardHousehold(t)
	occ := h.occurrence("Hoover the lounge") // 5 pts · £0.50, no adults' points sent
	r := wall.get("/board/occurrences/" + itoa(occ.ID) + "/pick")
	expect(t, r, http.StatusOK)
	for name, want := range map[string]string{"Alfie": "5 pts · £0.50", "Mum": "5 pts"} {
		i := strings.Index(r.body, "<span>"+name+"</span>")
		if i < 0 {
			t.Fatalf("%s not in the picker", name)
		}
		got, _, _ := strings.Cut(r.body[i:], "</label>")
		if !strings.Contains(got, `<small class="picker__worth">`+want+`</small>`) {
			t.Errorf("%s's worth: %q, want %q", name, got, want)
		}
	}
}

// Adults' points are saved for every way of choosing who does a chore, and
// a form without them gives adults the kids' points.
func TestChoreFormAdultPoints(t *testing.T) {
	h, _, phone := boardHousehold(t)
	if c := h.chore("Hoover the lounge"); c.AdultPoints != 5 {
		t.Errorf("defaulted adults' points = %d, want 5", c.AdultPoints)
	}
	mum := h.person("Mum")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Mow the lawn"}, "mode": {"assigned"}, "assignee_id": {itoa(mum.ID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"0"}, "adult_points": {"8"},
	}), "/admin/chores")
	if c := h.chore("Mow the lawn"); c.AdultPoints != 8 {
		t.Errorf("assigned adults' points = %d, want 8", c.AdultPoints)
	}

	// The form marks who's an adult, and the list shows an adult's chore at
	// its adults' points.
	expect(t, phone.get("/admin/chores/new"), http.StatusOK,
		`value="`+itoa(mum.ID)+`" data-kind="adult"`, `data-pay="kids"`, `data-pay="adults"`, `name="adult_points" value="0" min="0" max="100000" inputmode="numeric" data-follow`)
	list := phone.get("/admin/chores")
	i := strings.Index(list.body, "Mow the lawn")
	if i < 0 || !strings.Contains(list.body[i:i+600], `<span class="list__value">8 pts</span>`) {
		t.Errorf("list value for Mow the lawn not 8 pts")
	}
}

// Adults have no money: Admin › People shows points only and there's no
// Move to bank for them.
func TestAdultsHaveNoMoneyInAdmin(t *testing.T) {
	h, _, phone := boardHousehold(t)
	mum, alfie := h.person("Mum"), h.person("Alfie")
	for _, p := range []dbq.Person{mum, alfie} {
		if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
			HouseholdID: h.hh, PersonID: p.ID, Points: 7, Pence: 120, Kind: "completion", CreatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	r := phone.get("/admin/people")
	expect(t, r, http.StatusOK, "7 pts · £1.20")
	if strings.Count(r.body, "£1.20") != 1 {
		t.Errorf("£1.20 shown %d times on Admin › People, want once (Alfie only)", strings.Count(r.body, "£1.20"))
	}
	expect(t, phone.get("/admin/people/"+itoa(alfie.ID)), http.StatusOK, `id="bank"`)
	if r := phone.get("/admin/people/" + itoa(mum.ID)); strings.Contains(r.body, `id="bank"`) {
		t.Error("Move to bank shown for an adult")
	}
}
