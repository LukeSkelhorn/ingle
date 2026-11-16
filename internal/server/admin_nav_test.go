package server

import (
	"net/http"
	"strings"
	"testing"
)

// adminBar splits a page's admin bar into the links in the bar and the
// ⚙ Settings menu's links.
func adminBar(t *testing.T, r resp) (bar, menu string) {
	t.Helper()
	_, nav, ok := strings.Cut(r.body, `<nav class="admin__nav"`)
	if !ok {
		t.Fatal("no admin bar")
	}
	nav, _, _ = strings.Cut(nav, "</nav>")
	bar, menu, ok = strings.Cut(nav, `<details class="menu menu--nav">`)
	if !ok {
		t.Fatal("no ⚙ Settings menu")
	}
	return bar, menu
}

func TestAdminBarSettingsMenu(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()

	setModules(t, phone, modRewards, modPhotos, modCalendar)
	r := phone.get("/admin")
	expect(t, r, http.StatusOK)
	bar, menu := adminBar(t, r)
	for _, p := range []string{"/admin/people", "/admin/chores", "/admin/completions", "/admin/rewards"} {
		if !strings.Contains(bar, `href="`+p+`"`) {
			t.Errorf("bar has no %s", p)
		}
	}
	order := []string{"/admin/settings", "/admin/photos", "/admin/calendars", "/admin/devices", "/admin/modules"}
	at := -1
	for _, p := range order {
		i := strings.Index(menu, `href="`+p+`"`)
		if i < 0 {
			t.Errorf("menu has no %s", p)
		} else if i < at {
			t.Errorf("menu has %s out of order", p)
		}
		at = i
		if strings.Contains(bar, `href="`+p+`"`) {
			t.Errorf("bar has %s, want it only in the menu", p)
		}
	}
	if strings.Contains(bar+menu, "/admin/bank") {
		t.Error("Bank shows while My money is off")
	}

	// My money on: Bank is in the menu. Rewards off: it leaves the bar.
	setModules(t, phone, modMoney)
	bar, menu = adminBar(t, phone.get("/admin"))
	if !strings.Contains(menu, `href="/admin/bank"`) || strings.Contains(bar, "/admin/bank") {
		t.Error("Bank isn't in the menu with My money on")
	}
	if strings.Contains(bar+menu, "/admin/rewards") || strings.Contains(menu, "/admin/photos") {
		t.Error("switched-off modules still show")
	}
}

func TestAdminBarCurrentSection(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()

	r := phone.get("/admin/devices")
	expect(t, r, http.StatusOK, `class="admin__more" aria-label="Settings" aria-current="true"`)
	_, menu := adminBar(t, r)
	if !strings.Contains(menu, `<a href="/admin/devices" aria-current="page">Devices</a>`) {
		t.Error("Devices isn't marked current in the menu")
	}

	r = phone.get("/admin/people")
	expect(t, r, http.StatusOK, `<a href="/admin/people" aria-current="page">People</a>`)
	expectNot(t, r, `class="admin__more" aria-label="Settings" aria-current`)
}
