package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// order returns the titles in body, in the order they appear.
func order(body string, titles ...string) string {
	type at struct {
		i int
		t string
	}
	var found []at
	for _, t := range titles {
		if i := strings.Index(body, "<strong>"+t+"</strong>"); i >= 0 {
			found = append(found, at{i, t})
		}
	}
	for i := range found {
		for j := i + 1; j < len(found); j++ {
			if found[j].i < found[i].i {
				found[i], found[j] = found[j], found[i]
			}
		}
	}
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.t
	}
	return strings.Join(out, ",")
}

func TestChoresListFilterAndSort(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	for _, n := range []string{"Alfie", "Isla"} {
		expectPersonCreated(t, phone.post("/admin/people", url.Values{"name": {n}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}))
	}
	alfie, isla := h.person("Alfie"), h.person("Isla")
	for _, c := range []url.Values{
		{"title": {"Bins"}, "points": {"10"}, "money": {"0.50"}},
		{"title": {"Teeth"}, "points": {"10"}},
		{"title": {"Zoo trip"}, "money": {"1.00"}},
		{"title": {"Aardvark"}},
		{"title": {"Dishes"}, "points": {"5"}, "mode": {"pool"}},
	} {
		if c.Get("mode") == "" {
			c.Set("mode", "assigned")
			c.Set("assignee_id", itoa(alfie.ID))
		}
		c.Set("repeat", "daily")
		c.Set("starts_on", "2026-07-01")
		c.Set("suggest_offered", "1")
		expectRedirect(t, phone.post("/admin/chores", c), "/admin/chores")
	}
	all := []string{"Aardvark", "Bins", "Dishes", "Teeth", "Zoo trip"}

	r := phone.get("/admin/chores")
	expect(t, r, http.StatusOK, "<h1>Chores</h1>", `aria-current="true">Everyone`, "✋ Up for grabs", "Isla")
	if got := order(r.body, all...); got != "Aardvark,Bins,Dishes,Teeth,Zoo trip" {
		t.Errorf("by name: %s", got)
	}

	// One person.
	r = phone.get("/admin/chores?person=" + itoa(alfie.ID))
	expect(t, r, http.StatusOK, "Chores · 4 for Alfie", `href="/admin/chores/new?assignee=`+itoa(alfie.ID)+`&amp;back=`)
	if got := order(r.body, all...); got != "Aardvark,Bins,Teeth,Zoo trip" {
		t.Errorf("Alfie's: %s", got)
	}
	// Up for grabs.
	r = phone.get("/admin/chores?person=pool")
	expect(t, r, http.StatusOK, "Chores · 1 up for grabs")
	if got := order(r.body, all...); got != "Dishes" {
		t.Errorf("pool: %s", got)
	}
	// Nobody's: the empty state offers a chore for them.
	r = phone.get("/admin/chores?person=" + itoa(isla.ID))
	expect(t, r, http.StatusOK, "Chores · 0 for Isla", "No chores for Isla yet.", "Add a chore for Isla", "/admin/chores/new?assignee="+itoa(isla.ID))

	// Sorts, with ties on the other amount then the name; nothing-worth last.
	r = phone.get("/admin/chores?sort=points")
	if got := order(r.body, all...); got != "Bins,Teeth,Dishes,Zoo trip,Aardvark" {
		t.Errorf("by points: %s", got)
	}
	r = phone.get("/admin/chores?sort=money&person=" + itoa(alfie.ID))
	if got := order(r.body, all...); got != "Zoo trip,Bins,Teeth,Aardvark" {
		t.Errorf("Alfie's by money: %s", got)
	}
	// The chips keep the sort and the sort keeps the filter.
	expect(t, r, http.StatusOK, `href="/admin/chores?person=pool&amp;sort=money"`, `href="/admin/chores?sort=money"`,
		`<input type="hidden" name="person" value="`+itoa(alfie.ID)+`">`, `<option value="money" selected>`)

	// Nonsense is ignored.
	for _, q := range []string{"person=999", "person=abc", "sort=evil", "person=" + itoa(alfie.ID) + "x"} {
		r = phone.get("/admin/chores?" + q)
		expect(t, r, http.StatusOK, "<h1>Chores</h1>")
		if got := order(r.body, all...); got != "Aardvark,Bins,Dishes,Teeth,Zoo trip" {
			t.Errorf("%s: %s", q, got)
		}
	}
}

func TestChoresListBack(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expectPersonCreated(t, phone.post("/admin/people", url.Values{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}))
	alfie := h.person("Alfie")
	back := "/admin/chores?person=" + itoa(alfie.ID) + "&sort=money"

	// "Add a chore for Alfie" pre-fills him and comes back to his list.
	r := phone.get("/admin/chores/new?" + url.Values{"assignee": {itoa(alfie.ID)}, "back": {back}}.Encode())
	expect(t, r, http.StatusOK, `value="assigned" checked`, `name="assignee_id" value="`+itoa(alfie.ID)+`" checked`,
		`<a href="/admin/chores?person=`+itoa(alfie.ID)+`&amp;sort=money">‹ Chores</a>`,
		`name="back" value="/admin/chores?person=`+itoa(alfie.ID)+`&amp;sort=money"`)
	chore := url.Values{
		"title": {"Bins"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "suggest_offered": {"1"}, "back": {back},
	}
	expectRedirect(t, phone.post("/admin/chores", chore), back)
	bins := h.chore("Bins")

	// The list links into the chore with back, and the edit page keeps it,
	// including after saving.
	expect(t, phone.get(back), http.StatusOK, `href="/admin/chores/`+itoa(bins.ID)+`?back=%2Fadmin%2Fchores%3Fperson%3D`+itoa(alfie.ID)+`%26sort%3Dmoney"`)
	expect(t, phone.get("/admin/chores/"+itoa(bins.ID)+"?"+url.Values{"back": {back}}.Encode()), http.StatusOK,
		`‹ Chores`, `/admin/chores?person=`+itoa(alfie.ID)+`&amp;sort=money">‹ Chores`)
	chore.Set("title", "Bins out")
	expect(t, phone.post("/admin/chores/"+itoa(bins.ID), chore), http.StatusOK, "Saved.", `&amp;sort=money">‹ Chores`)

	// Elsewhere is refused.
	for _, bad := range []string{"https://evil.example/admin/chores", "//evil.example/admin/chores", "/admin/people", "/admin/chores?person=999"} {
		chore.Set("title", "Teeth "+bad)
		chore.Set("back", bad)
		expectRedirect(t, phone.post("/admin/chores", chore), "/admin/chores")
	}
	// An unknown assignee isn't pre-filled.
	if r := phone.get("/admin/chores/new?assignee=999"); strings.Contains(r.body, `value="999" checked`) {
		t.Error("unknown assignee pre-filled")
	}

	// Archive comes back to the filtered list.
	expectRedirect(t, phone.post("/admin/chores/"+itoa(bins.ID)+"/archive", url.Values{"back": {back}}), back)
}
