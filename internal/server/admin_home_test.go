package server

import (
	"maps"
	"net/http"
	"net/url"
	"testing"
)

const (
	addFamily = "Add your family"
	setUpWall = "Set up the wall display"
)

// Admin's home suggests getting started until it's done: adding the
// family while there's one person, and a display while there's none.
func TestGettingStartedNudges(t *testing.T) {
	h := newHarness(t)
	values := maps.Clone(setupValues)
	values.Del("use_as_display")
	values.Del("display_name")
	phone := h.device()
	expectRedirect(t, phone.post("/setup", values), "/admin")

	expect(t, phone.get("/admin"), http.StatusOK, addFamily, `href="/admin/people/new"`, setUpWall, `href="/admin/devices"`)

	expectPersonCreated(t, phone.post("/admin/people", url.Values{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}))
	home := phone.get("/admin")
	expect(t, home, http.StatusOK, setUpWall)
	expectNot(t, home, addFamily)

	// The wall tablet becomes a display: no more nudge, there or here.
	wall := h.device()
	expectRedirect(t, wall.post("/signin", url.Values{"email": {"mum@example.com"}, "password": {"correct horse battery"}}), "/admin")
	expectRedirect(t, wall.post("/admin/devices/register", url.Values{"name": {"Kitchen"}}), "/")
	expectNot(t, phone.get("/admin"), addFamily, setUpWall)
}

// On a display, admin doesn't ask to set up a display.
func TestNoDisplayNudgeOnADisplay(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp() // registers this device as the display, unlocked
	home := wall.get("/admin")
	expect(t, home, http.StatusOK, addFamily)
	expectNot(t, home, setUpWall)
}
