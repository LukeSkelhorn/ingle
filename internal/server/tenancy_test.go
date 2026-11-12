package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/module"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// twoHouseholds is one server holding two households, told apart by the
// Host header: a.test is the harness's own household and b.test a second
// one. Both are set up, with a child and a chore each.
type twoHouseholds struct {
	h        *harness
	a, b     int64
	aW, bW   *device // each household's display, unlocked by its adult
	beaID    int64
	walkID   int64
	alfieID  int64
	feedID   int64
	dadEmail string
}

func newTwoHouseholds(t *testing.T) *twoHouseholds {
	t.Helper()
	h := newHarness(t)
	ctx := t.Context()
	b, err := h.st.Q.CreateHousehold(store.AsSystem(ctx), dbq.CreateHouseholdParams{Name: "Home", CreatedAt: h.clock().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.srv.Settings.Ensure(store.WithHousehold(ctx, b.ID), b.ID, h.srv.Locale.Location); err != nil {
		t.Fatal(err)
	}
	hosts := map[string]int64{"a.test": h.hh, "b.test": b.ID}
	h.srv.ResolveHousehold = func(r *http.Request) (int64, bool, error) {
		id, ok := hosts[r.Host]
		return id, ok, nil
	}
	h.srv.Households = func(context.Context) ([]int64, error) { return []int64{h.hh, b.ID}, nil }

	x := &twoHouseholds{h: h, a: h.hh, b: b.ID, dadEmail: "dad@example.org"}
	x.aW = x.on("a.test")
	expectRedirect(t, x.aW.post("/setup", setupValues), "/admin")
	bSetup := url.Values{}
	for k, v := range setupValues {
		bSetup[k] = v
	}
	bSetup.Set("household_name", "The Joneses")
	bSetup.Set("name", "Dad")
	bSetup.Set("email", x.dadEmail)
	x.bW = x.on("b.test")
	expectRedirect(t, x.bW.post("/setup", bSetup), "/admin")

	expectPersonCreated(t, x.aW.post("/admin/people", url.Values{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}))
	expectPersonCreated(t, x.bW.post("/admin/people", url.Values{"name": {"Bea"}, "kind": {"child"}, "colour": {"#E54666"}, "avatar_mode": {"none"}}))
	x.alfieID = x.personIn(x.a, "Alfie").ID
	x.beaID = x.personIn(x.b, "Bea").ID
	expectRedirect(t, x.aW.post("/admin/chores", url.Values{
		"title": {"Feed the cat"}, "mode": {"assigned"}, "assignee_id": {itoa(x.alfieID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"2"},
	}), "/admin/chores")
	expectRedirect(t, x.bW.post("/admin/chores", url.Values{
		"title": {"Walk the dog"}, "mode": {"assigned"}, "assignee_id": {itoa(x.beaID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"3"},
	}), "/admin/chores")
	x.feedID = x.choreIn(x.a, "Feed the cat").ID
	x.walkID = x.choreIn(x.b, "Walk the dog").ID
	return x
}

// on is a new browser whose requests go to host.
func (x *twoHouseholds) on(host string) *device {
	d := x.h.device()
	d.host = host
	return d
}

func (x *twoHouseholds) personIn(hh int64, name string) dbq.Person {
	x.h.t.Helper()
	for _, p := range must(x.h.st.Q.ListPeople(store.WithHousehold(x.h.t.Context(), hh), hh)) {
		if p.Name == name {
			return p
		}
	}
	x.h.t.Fatalf("no person %q in household %d", name, hh)
	return dbq.Person{}
}

func (x *twoHouseholds) choreIn(hh int64, title string) dbq.Chore {
	x.h.t.Helper()
	for _, c := range must(x.h.st.Q.ListChores(store.WithHousehold(x.h.t.Context(), hh), hh)) {
		if c.Title == title {
			return c
		}
	}
	x.h.t.Fatalf("no chore %q in household %d", title, hh)
	return dbq.Chore{}
}

func expectNot(t *testing.T, r resp, absent ...string) {
	t.Helper()
	for _, s := range absent {
		if strings.Contains(r.body, s) {
			t.Errorf("body has %q, want it absent", s)
		}
	}
}

func TestTenancyEachHouseholdSeesOnlyItsOwn(t *testing.T) {
	x := newTwoHouseholds(t)

	for _, path := range []string{"/board", "/admin/people", "/admin/chores", "/admin"} {
		a, b := x.aW.get(path), x.bW.get(path)
		expect(t, a, http.StatusOK)
		expect(t, b, http.StatusOK)
		expectNot(t, a, "Bea", "Walk the dog", "The Joneses", "Dad")
		expectNot(t, b, "Alfie", "Feed the cat", "The Smiths", "Mum")
	}
	expect(t, x.aW.get("/board"), http.StatusOK, "Alfie", "Feed the cat")
	expect(t, x.bW.get("/board"), http.StatusOK, "Bea", "Walk the dog")
}

func TestTenancyOtherHouseholdsIDsAreNotFound(t *testing.T) {
	x := newTwoHouseholds(t)

	expect(t, x.aW.get("/admin/people/"+itoa(x.beaID)), http.StatusNotFound)
	expect(t, x.aW.get("/admin/chores/"+itoa(x.walkID)), http.StatusNotFound)
	// The board treats an unknown person as one archived since it loaded.
	r := x.aW.get("/board/people/" + itoa(x.beaID))
	expect(t, r, http.StatusConflict)
	expectNot(t, r, "Bea")

	expect(t, x.aW.post("/admin/chores/"+itoa(x.walkID)+"/archive", url.Values{}), http.StatusNotFound)
	expect(t, x.aW.post("/admin/people/"+itoa(x.beaID)+"/archive", url.Values{}), http.StatusNotFound)
	expect(t, x.aW.post("/admin/people/"+itoa(x.beaID)+"/points", url.Values{"points": {"50"}, "note": {"Sneaky"}}), http.StatusNotFound)

	// B's chore and child are untouched.
	if c := x.choreIn(x.b, "Walk the dog"); c.ArchivedAt != nil {
		t.Error("A archived B's chore")
	}
	x.personIn(x.b, "Bea") // still listed, so not archived
	expect(t, x.bW.get("/board"), http.StatusOK, "Bea", "Walk the dog")
	expectNot(t, x.bW.get("/board/people/"+itoa(x.beaID)), "Sneaky")
}

func TestTenancyCookiesStayInTheirHousehold(t *testing.T) {
	x := newTwoHouseholds(t)

	// A's display, pointed at B's address: its cookies are dropped.
	x.aW.host = "b.test"
	expectRedirect(t, x.aW.get("/board"), "/signin")
	expectRedirect(t, x.aW.get("/admin"), "/signin")

	// The right password for B's adult, on A's address, doesn't sign in.
	phone := x.on("a.test")
	r := phone.post("/signin", url.Values{"email": {x.dadEmail}, "password": {"correct horse battery"}})
	expect(t, r, http.StatusUnauthorized, "don&#39;t match")
	expectRedirect(t, phone.get("/admin"), "/signin")

	// Setup is closed for both, once each is set up.
	expect(t, x.on("a.test").get("/setup"), http.StatusNotFound)
	expect(t, x.on("b.test").get("/setup"), http.StatusNotFound)
}

func TestTenancyUnknownAddressFollowsTheSession(t *testing.T) {
	x := newTwoHouseholds(t)

	// An address the resolver doesn't know: no household until sign-in.
	d := x.on("")
	expectRedirect(t, d.get("/"), "/signin")
	expect(t, d.get("/setup"), http.StatusNotFound)
	expect(t, d.get("/signin"), http.StatusOK)

	// Signing in picks the household from the account.
	expectRedirect(t, d.post("/signin", url.Values{"email": {x.dadEmail}, "password": {"correct horse battery"}}), "/admin")
	r := d.get("/admin/people")
	expect(t, r, http.StatusOK, "Bea")
	expectNot(t, r, "Alfie")
}

func TestTenancyModuleLoopsRunForEveryHousehold(t *testing.T) {
	x := newTwoHouseholds(t)

	got := map[int64]bool{}
	hhs, err := x.h.srv.households(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, hh := range hhs {
		if x.h.srv.moduleOn(withHousehold(t.Context(), hh), modCalendar) {
			got[hh] = true
		}
	}
	if !got[x.a] || !got[x.b] {
		t.Fatalf("calendar on for %v, want both %d and %d", got, x.a, x.b)
	}
}

func TestAdminLinksShowInTheAdminBar(t *testing.T) {
	h := newHarness(t)
	h.srv.AdminLinks = []module.NavItem{{Key: "/admin/billing", Label: "Billing", Path: "/admin/billing"}}
	d := h.setUp()
	expect(t, d.get("/admin"), http.StatusOK, `<a href="/admin/billing">Billing</a>`)
}
