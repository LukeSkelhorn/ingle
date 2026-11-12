package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/module"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// setModules saves Admin › Modules with just ids ticked.
func setModules(t *testing.T, phone *device, ids ...string) resp {
	t.Helper()
	r := phone.post("/admin/modules", url.Values{"module": ids})
	expect(t, r, http.StatusOK)
	return r
}

func notContains(t *testing.T, r resp, what ...string) {
	t.Helper()
	for _, w := range what {
		if strings.Contains(r.body, w) {
			t.Errorf("body contains %q", w)
		}
	}
}

func TestModulesPage(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()

	// A new household starts with its defaults and isn't asked.
	r := phone.get("/admin")
	expect(t, r, http.StatusOK, `href="/admin/modules"`, `href="/admin/rewards"`, `href="/admin/photos"`, `href="/admin/calendars"`)
	notContains(t, r, "Choose modules")
	expect(t, phone.get("/admin/modules"), http.StatusOK,
		`value="rewards" checked`, `value="photos" checked`, `value="calendar" checked`, "its data is kept")

	// A household from before modules: everything on, and it's asked.
	if _, err := h.st.SQL().ExecContext(h.ctx(), "DELETE FROM household_modules"); err != nil {
		t.Fatal(err)
	}
	h.srv.Modules.Invalidate(h.hh)
	r = phone.get("/admin")
	expect(t, r, http.StatusOK, "Choose modules", "switch whole features off: Rewards, My money, Photos and Calendar.", `href="/admin/rewards"`, `href="/admin/calendars"`)
	expect(t, phone.get("/admin/modules"), http.StatusOK, "Everything is on until you save this page", `value="photos" checked`)

	// Saving once answers it.
	expect(t, setModules(t, phone, "rewards", "photos"), http.StatusOK, "Saved.", `value="rewards" checked`)
	r = phone.get("/admin")
	notContains(t, r, "Choose modules", `href="/admin/calendars"`)
	expect(t, r, http.StatusOK, "changed the modules")

	// Only adults.
	expectRedirect(t, h.device().get("/admin/modules"), "/signin")
}

func TestRewardsModuleOff(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	sheet := "/board/people/" + itoa(alfie.ID)
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Screen time"}, "cost_points": {"30"}}), "/admin/rewards")
	reward := must(h.st.Q.ListRewards(h.ctx(), h.hh))[0]
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 70, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	expect(t, wall.post("/board/rewards/"+itoa(reward.ID)+"/claim", url.Values{"person": {itoa(alfie.ID)}}),
		http.StatusOK, "🎁 1 waiting")
	claim := itoa(must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))[0].ID)
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Rewards waiting")

	setModules(t, phone, "photos", "calendar")

	// Its pages are gone…
	for _, p := range []string{"/admin/rewards", "/admin/rewards/new", "/admin/rewards/" + itoa(reward.ID)} {
		expect(t, phone.get(p), http.StatusNotFound)
	}
	expect(t, phone.post("/admin/rewards/claims/"+claim+"/approve", nil), http.StatusNotFound)
	expect(t, wall.post("/board/rewards/"+itoa(reward.ID)+"/claim", url.Values{"person": {itoa(alfie.ID)}}), http.StatusNotFound)
	expect(t, wall.post(sheet+"/goal", url.Values{"reward": {itoa(reward.ID)}}), http.StatusNotFound)
	expect(t, wall.get("/board/redemptions/"+claim+"/review"), http.StatusNotFound)
	expect(t, wall.post("/board/redemptions/"+claim+"/cancel", nil), http.StatusNotFound)

	// …and so are its links and board pieces. Points stay.
	r := phone.get("/admin")
	expect(t, r, http.StatusOK, `href="/admin/photos"`)
	notContains(t, r, `href="/admin/rewards"`)
	notContains(t, phone.get("/admin/completions"), "Rewards waiting")
	notContains(t, phone.get("/admin/people/"+itoa(alfie.ID)), "Saving for")
	notContains(t, wall.get("/board"), "waiting")
	r = wall.get(sheet)
	expect(t, r, http.StatusOK, "40 pts")
	notContains(t, r, "Waiting for a grown-up", `<h3 class="person__sub">Rewards</h3>`, "/claim")

	// Turning it back on brings the waiting claim back.
	setModules(t, phone, "rewards", "photos", "calendar")
	expect(t, wall.get("/board"), http.StatusOK, "🎁 1 waiting")
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Rewards waiting", "Screen time")
	expect(t, phone.get("/admin/rewards"), http.StatusOK, "Screen time")
}

func TestPhotosModuleOff(t *testing.T) {
	_, wall, phone := boardHousehold(t)
	expect(t, wall.get("/screensaver/photos"), http.StatusOK, `"photos"`)

	setModules(t, phone, "rewards", "calendar")
	expect(t, wall.get("/screensaver/photos"), http.StatusNotFound)
	expect(t, wall.get("/photos/u/abc.jpg"), http.StatusNotFound)
	expect(t, phone.get("/admin/photos"), http.StatusNotFound)
	notContains(t, phone.get("/admin"), `href="/admin/photos"`)
	// The screensaver itself stays: clock and chores.
	expect(t, wall.get("/screensaver"), http.StatusOK, "saver__time", "0 of 1")
}

func TestCalendarModuleOff(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Europe/London:20260701T090000\r\nDTEND;TZID=Europe/London:20260701T100000\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {h.ics.URL + "/basic.ics"}}),
		http.StatusOK, "Family added.")
	expect(t, wall.get("/screensaver"), http.StatusOK, "Swimming")
	expect(t, wall.get("/board/calendar"), http.StatusOK, "Swimming", "Feed the cat")

	setModules(t, phone, "rewards", "photos")
	expect(t, phone.get("/admin/calendars"), http.StatusNotFound)
	expect(t, phone.post("/admin/calendars/refresh", nil), http.StatusNotFound)
	notContains(t, phone.get("/admin"), `href="/admin/calendars"`)
	r := wall.get("/screensaver")
	expect(t, r, http.StatusOK, "saver__time")
	notContains(t, r, "Swimming", "Nothing else on today")
	// The board calendar stays, with chores only and no nudge to add feeds.
	r = wall.get("/board/calendar")
	expect(t, r, http.StatusOK, "Feed the cat")
	notContains(t, r, "Swimming", "Add calendar feeds")

	setModules(t, phone, "rewards", "photos", "calendar")
	expect(t, wall.get("/board/calendar"), http.StatusOK, "Swimming")
}

// testModule is a module for tests: it needs others, has a page and a loop.
type testModule struct {
	id       string
	requires []string
	runs     *atomic.Int32
}

func (m testModule) ID() string                 { return m.id }
func (m testModule) Name() string               { return "Pocket money" }
func (m testModule) Description() string        { return "For tests." }
func (m testModule) Requires() []string         { return m.requires }
func (m testModule) DefaultOn() bool            { return false }
func (m testModule) AdminNav() []module.NavItem { return nil }
func (m testModule) Routes(r module.Router) {
	r.Viewer("GET /test/"+m.id, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) })
}

func (m testModule) Loops() []module.Loop {
	return []module.Loop{{Name: "count", Every: time.Hour, Run: func(context.Context, int64) error {
		m.runs.Add(1)
		return nil
	}}}
}

func TestModuleRequirements(t *testing.T) {
	runs := &atomic.Int32{}
	h := newHarnessWith(t, testModule{id: "pocket", requires: []string{modRewards}, runs: runs})
	h.setUp()
	phone := h.phone()
	wall := h.setUpDisplayLocked()

	// It starts off: its route is hidden.
	expect(t, wall.get("/test/pocket"), http.StatusNotFound)
	expect(t, phone.get("/admin/modules"), http.StatusOK, "Pocket money", "Needs Rewards.")

	// Its loop doesn't run while it's off, and runs as soon as it's on.
	ctx, cancel := context.WithCancel(h.ctx())
	done := make(chan struct{})
	go func() { h.srv.RunModuleLoops(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return h.srv.loopsStarted.Load() })
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("loop ran %d times while off", n)
	}

	// Turning it on with Rewards off turns Rewards on too.
	setModules(t, phone, "photos", "calendar")
	r := setModules(t, phone, "pocket", "photos", "calendar")
	expect(t, r, http.StatusOK, "Rewards is on too", `value="rewards" checked`, `value="pocket" checked`)
	expect(t, wall.get("/test/pocket"), http.StatusOK, "hello")
	waitFor(t, func() bool { return runs.Load() == 1 })

	// Turning Rewards off asks first, and changes nothing until confirmed.
	r = setModules(t, phone, "pocket", "photos", "calendar")
	expect(t, r, http.StatusOK, "Pocket money needs a module you're turning off", `name="confirm"`)
	expect(t, phone.get("/admin/rewards"), http.StatusOK)
	expect(t, phone.post("/admin/modules", url.Values{"module": {"pocket", "photos", "calendar"}, "confirm": {"1"}}),
		http.StatusOK, "Saved.")
	expect(t, phone.get("/admin/rewards"), http.StatusNotFound)
	expect(t, wall.get("/test/pocket"), http.StatusNotFound)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
