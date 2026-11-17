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

	wall := h.device()
	expectRedirect(t, wall.post("/signin", url.Values{"email": {"mum@example.com"}, "password": {"correct horse battery"}}), "/admin")
	expectRedirect(t, wall.post("/admin/devices/register", url.Values{"name": {"Kitchen"}}), "/")
	expectNot(t, phone.get("/admin"), addFamily, setUpWall)
}

func TestNoDisplayNudgeOnADisplay(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	home := wall.get("/admin")
	expect(t, home, http.StatusOK, addFamily)
	expectNot(t, home, setUpWall)
}
