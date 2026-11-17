package server

import (
	"bufio"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func lockStream(t *testing.T, d *device) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.h.ts.URL+"/board/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	out := make(chan string, 16)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(res.Body)
		inLock := false
		for sc.Scan() {
			l := sc.Text()
			switch {
			case l == "event: lock":
				inLock = true
			case inLock && strings.HasPrefix(l, "data: "):
				out <- strings.TrimPrefix(l, "data: ")
				inLock = false
			case l == "":
				inLock = false
			}
		}
	}()
	return out
}

func nextLock(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("no lock event")
		return ""
	}
}

func TestLockedDisplayShowsNothingPersonal(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	events := lockStream(t, wall)
	if got := nextLock(t, events); got != "unlocked" {
		t.Fatalf("first lock event = %q", got)
	}

	board := wall.get("/")
	expect(t, board, http.StatusOK, `aria-label="Lock the displays"`, "The Smiths")
	expectRedirect(t, wall.post("/lock", nil), "/")
	if got := nextLock(t, events); got != "locked" {
		t.Fatalf("lock event = %q", got)
	}

	locked := wall.get("/")
	expect(t, locked, http.StatusOK, "unlocking?<", `class="people-grid"`, `data-person="1"`, "Hi Mum", `data-length="4"`, "pinpad__keys", "/static/pinpad.js", "saver__time")
	for _, secret := range []string{"The Smiths", "/avatars/", `id="board"`} {
		if strings.Contains(locked.body, secret) {
			t.Errorf("locked page shows %q", secret)
		}
	}
	for _, p := range []string{"/board", "/screensaver", "/avatars/x.jpg", "/board/occurrences/1/pick", "/board/people/1", "/board/redemptions/1/review"} {
		if r := wall.get(p); r.status != http.StatusLocked {
			t.Errorf("GET %s = %d, want 423", p, r.status)
		}
	}
	req, _ := http.NewRequestWithContext(h.ctx(), http.MethodGet, h.ts.URL+"/board", http.NoBody)
	req.Header.Set("HX-Request", "true")
	if r := wall.do(req); r.status != http.StatusLocked || r.header.Get("HX-Redirect") != "/" {
		t.Errorf("htmx /board while locked = %d %q", r.status, r.header.Get("HX-Redirect"))
	}
	for _, p := range []string{"/board/occurrences/1/complete", "/board/people/1/goal", "/board/people/1/goal/clear"} {
		if r := wall.post(p, nil); r.status != http.StatusLocked {
			t.Errorf("POST %s while locked = %d", p, r.status)
		}
	}
	expect(t, wall.get("/clock?big=1"), http.StatusOK, "saver__time")
	expect(t, wall.get("/screensaver/photos"), http.StatusOK)
	expectRedirect(t, wall.get("/admin"), "/admin/unlock")
	expectRedirect(t, wall.get("/admin/unlock"), "/")

	expect(t, phone.get("/"), http.StatusOK, "The displays are locked", "The Smiths")
	expect(t, phone.get("/admin"), http.StatusOK, "Unlock the displays", "Mum</strong> locked the displays")

	expect(t, wall.post("/unlock", url.Values{"person": {"1"}, "code": {"1111"}}), http.StatusUnauthorized, "Wrong PIN")
	expect(t, wall.post("/unlock", url.Values{"person": {"999"}, "code": {"2468"}}), http.StatusUnauthorized, "Wrong PIN")
	expect(t, wall.get("/"), http.StatusOK, "unlocking?")

	expect(t, wall.post("/unlock", url.Values{"code": {"0000"}}), http.StatusUnauthorized, "Wrong code")
	expect(t, wall.get("/"), http.StatusOK, "unlocking?<")
	expect(t, wall.post("/unlock", url.Values{"person": {"1"}, "code": {"2468"}}), http.StatusNoContent)
	if got := nextLock(t, events); got != "unlocked" {
		t.Fatalf("unlock event = %q", got)
	}
	expect(t, wall.get("/"), http.StatusOK, "The Smiths", `id="board"`)
	expectRedirect(t, wall.get("/admin"), "/admin/unlock")
}

func TestHouseholdCodeAndPhone(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()

	r := phone.get("/admin/settings")
	expect(t, r, http.StatusOK, "No code yet", `data-dialog="code-dialog">Set a code</a>`,
		`<dialog class="dialog" id="code-dialog" aria-labelledby="code-dialog-h">`,
		`name="code"`, `name="code_confirm"`, "data-pinpad-set", "/static/pinpad.js", "/static/dialogs.js")
	expectNot(t, r, "remove-code-dialog")
	expect(t, phone.get("/admin/settings?edit=code"), http.StatusOK, `id="code-dialog" aria-labelledby="code-dialog-h" open data-open>`)
	expect(t, phone.post("/admin/settings/unlock-code", url.Values{"code": {"1357"}, "code_confirm": {"1358"}}),
		http.StatusUnprocessableEntity, "The codes don&#39;t match.", `id="code-dialog" aria-labelledby="code-dialog-h" open data-open>`)
	expect(t, phone.post("/admin/settings/unlock-code", url.Values{"code": {"12"}, "code_confirm": {"12"}}),
		http.StatusUnprocessableEntity, "Use 4 to 6 digits.", "open data-open>")
	r = phone.post("/admin/settings/unlock-code", url.Values{"code": {"1357"}, "code_confirm": {"1357"}})
	expect(t, r, http.StatusOK, "Unlock code saved.", "Code is set", ">Change code</a>", `data-dialog="remove-code-dialog">Remove</a>`,
		"Change the household code", "Remove the household code?")
	expectNot(t, r, "open data-open>")
	expect(t, phone.get("/admin/settings?edit=remove-code"), http.StatusOK,
		`id="remove-code-dialog" aria-labelledby="remove-code-dialog-h" open data-open>`, "Only adult PINs will unlock the displays.")

	expectRedirect(t, phone.post("/lock", url.Values{"next": {"/admin"}}), "/admin")
	expect(t, wall.get("/"), http.StatusOK, "unlocking?<")
	expect(t, wall.post("/unlock", url.Values{"code": {"1357"}}), http.StatusNoContent)
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)
	expect(t, phone.get("/admin"), http.StatusOK, "Someone</strong> unlocked the displays with the household code")

	expectRedirect(t, wall.post("/lock", nil), "/")
	expectRedirect(t, phone.post("/unlock", nil), "/")
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)

	expectRedirect(t, wall.post("/lock", nil), "/")
	for range 4 {
		expect(t, wall.post("/unlock", url.Values{"code": {"9999"}}), http.StatusUnauthorized)
	}
	expect(t, wall.post("/unlock", url.Values{"code": {"9999"}}), http.StatusTooManyRequests, "Too many tries")
	expect(t, wall.post("/unlock", url.Values{"code": {"1357"}}), http.StatusTooManyRequests)
	h.advance(6 * time.Minute)
	expect(t, wall.post("/unlock", url.Values{"code": {"1357"}}), http.StatusNoContent)

	expect(t, phone.post("/admin/settings/unlock-code/clear", nil), http.StatusOK, "Unlock code removed")
	expectRedirect(t, wall.post("/lock", nil), "/")
	expect(t, wall.post("/unlock", url.Values{"code": {"1357"}}), http.StatusUnauthorized)
	expect(t, wall.post("/unlock", url.Values{"code": {"2468"}}), http.StatusNoContent)
}

func TestNoLockWithoutAWayBackIn(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	adults, err := h.st.Q.ListUnlockPINs(h.ctx(), h.hh)
	if err != nil || len(adults) != 1 {
		t.Fatalf("adults = %v, %v", adults, err)
	}
	if err := h.st.Q.ClearPersonPIN(h.ctx(), dbq.ClearPersonPINParams{HouseholdID: h.hh, ID: adults[0].ID}); err != nil {
		t.Fatal(err)
	}
	board := wall.get("/")
	expect(t, board, http.StatusOK, `id="board"`)
	if strings.Contains(board.body, `aria-label="Lock the displays"`) {
		t.Error("lock button shown with no code or PIN")
	}
	expect(t, wall.post("/lock", nil), http.StatusConflict, "household unlock code")
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)
	phone := h.phone()
	expect(t, phone.get("/"), http.StatusOK, `aria-label="Lock the displays"`)
}

func TestScheduleLocksDisplays(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"5"}, "lock_idle_hours": {"0"},
		"lock_day": {"0", "1", "2", "3", "4", "5", "6"}, "lock_from": {"00:45"}, "lock_until": {"06:00"},
	}), http.StatusOK, "Settings saved.", `value="3" checked`)
	tick := func() {
		t.Helper()
		if err := h.lock.Tick(h.ctx(), h.hh); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)
	h.advance(20 * time.Minute)
	tick()
	expect(t, wall.get("/"), http.StatusOK, "unlocking?<")
	expect(t, phone.get("/admin"), http.StatusOK, "Ingle</strong> locked the displays on schedule")
	h.advance(6 * time.Hour)
	tick()
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)
}

func TestIdleLockCountsTaps(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"5"}, "lock_idle_hours": {"2"},
		"lock_from": {"09:00"}, "lock_until": {"15:00"},
	}), http.StatusOK, "Settings saved.")
	tick := func() {
		t.Helper()
		if err := h.lock.Tick(h.ctx(), h.hh); err != nil {
			t.Fatal(err)
		}
	}
	h.advance(90 * time.Minute)
	expect(t, wall.post("/activity", nil), http.StatusNoContent)
	h.advance(100 * time.Minute)
	wall.get("/clock")
	expect(t, phone.get("/"), http.StatusOK)
	tick()
	expect(t, wall.get("/"), http.StatusOK, `id="board"`)
	h.advance(2 * time.Hour)
	tick()
	expect(t, wall.get("/"), http.StatusOK, "unlocking?<")
}
