package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

var countdownToken = regexp.MustCompile(`\?t=([A-Za-z0-9_-]+)`)

// The board day in the tests is Wed 1 Jul 2026.

func TestCountdownsAdmin(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, isla := h.person("Alfie"), h.person("Isla")

	expect(t, phone.get("/admin/countdowns"), http.StatusOK, "No countdowns yet", `href="/admin/countdowns/new"`)
	expect(t, phone.get("/admin"), http.StatusOK, `href="/admin/countdowns"`)
	expect(t, phone.get("/admin/countdowns/new"), http.StatusOK, `name="on_date"`, `name="yearly"`, `type="checkbox" name="people_ids"`, "🎂")

	// Errors keep what was typed.
	expect(t, phone.post("/admin/countdowns", url.Values{"title": {"Seaside"}, "emoji": {"🏖️"}, "on_date": {"2026-06-01"}}),
		http.StatusUnprocessableEntity, "That day has already been", `value="Seaside"`)
	// No emoji chosen: it offers one first.
	expect(t, phone.post("/admin/countdowns", url.Values{"title": {"Holiday to Spain"}, "on_date": {"2026-07-04"}, "people_ids": {itoa(alfie.ID)}}),
		http.StatusUnprocessableEntity, "We picked 🏖️ for this countdown")
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Holiday to Spain"}, "emoji": {"🏖️"}, "suggest_offered": {"1"}, "on_date": {"2026-07-04"},
		"people_ids": {itoa(alfie.ID), itoa(isla.ID)},
	}), "/admin/countdowns?flash=Added+Holiday+to+Spain.")
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Christmas"}, "emoji": {"🎄"}, "on_date": {"2025-12-25"}, "yearly": {"1"},
	}), "/admin/countdowns?flash=Added+Christmas.")
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Sports day"}, "emoji": {"⚽"}, "on_date": {"2026-07-10"},
	}), "/admin/countdowns?flash=Added+Sports+day.")
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Panto"}, "emoji": {"🎭"}, "on_date": {"2026-12-20"},
	}), "/admin/countdowns?flash=Added+Panto.")

	// A birthday from People.
	expect(t, phone.get("/admin/people/"+itoa(alfie.ID)), http.StatusOK, `name="birth_day"`, `name="birth_month"`, `name="can_add_countdowns"`)
	r := phone.post("/admin/people/"+itoa(alfie.ID), url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"},
		"countdowns_shown": {"1"}, "birth_day": {"31"}, "birth_month": {"6"}, "birth_year": {"2017"},
	})
	expect(t, r, http.StatusUnprocessableEntity, "That day isn&#39;t in June.")
	r = phone.post("/admin/people/"+itoa(alfie.ID), url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"},
		"countdowns_shown": {"1"}, "birth_day": {"2"}, "birth_month": {"7"}, "birth_year": {"2017"},
	})
	expect(t, r, http.StatusOK, "Saved.", `<option value="2" selected>2</option>`, `<option value="7" selected>July</option>`, `value="2017"`)

	list := phone.get("/admin/countdowns")
	expect(t, list, http.StatusOK, "Alfie&#39;s birthday", "turns 9", "Tomorrow", "Holiday to Spain", "3 days", "Christmas", "Every year")
	if a, b := strings.Index(list.body, "Alfie&#39;s birthday"), strings.Index(list.body, "Holiday to Spain"); a < 0 || a > b {
		t.Error("the birthday isn't first")
	}

	// The board: a strip with the next three, and the view with them all.
	wall.get("/") // nothing cached
	board := wall.get("/board")
	expect(t, board, http.StatusOK, `class="countdown-strip"`, `data-view-to="countdowns"`,
		"Alfie&#39;s birthday", "Holiday to Spain", "Sports day")
	notContains(t, board, "Christmas", "Panto")
	expect(t, wall.get("/"), http.StatusOK, `data-panel="countdowns"`, `<span>Countdowns</span>`)
	view := wall.get("/board/countdowns")
	expect(t, view, http.StatusOK, "Alfie&#39;s birthday", "turns 9", "Holiday to Spain", "<b>3</b>", "Christmas", "Panto", "Fri 25 Dec")
	notContains(t, view, "+ Add a countdown") // no child may yet
	expect(t, wall.get("/screensaver"), http.StatusOK, "Alfie&#39;s birthday tomorrow")

	// Edit and delete.
	spain := countdownNamed(t, h, "Holiday to Spain")
	expect(t, phone.get("/admin/countdowns/"+itoa(spain.ID)), http.StatusOK, `value="Holiday to Spain"`, `value="2026-07-04"`,
		`name="people_ids" value="`+itoa(alfie.ID)+`" data-kind="child" checked`,
		`name="people_ids" value="`+itoa(isla.ID)+`" data-kind="child" checked`)
	expect(t, wall.get("/board/countdowns"), http.StatusOK, "Alfie and Isla")
	expectRedirect(t, phone.post("/admin/countdowns/"+itoa(spain.ID), url.Values{
		"title": {"Spain!"}, "emoji": {"✈️"}, "on_date": {"2026-07-05"},
	}), "/admin/countdowns?flash=Saved+Spain%21.")
	if c := countdownNamed(t, h, "Spain!"); c.OnDate != "2026-07-05" {
		t.Errorf("after edit: %+v", c)
	} else if ids := must(h.st.Q.ListPeopleForCountdown(h.ctx(), dbq.ListPeopleForCountdownParams{HouseholdID: h.hh, CountdownID: c.ID})); len(ids) != 0 {
		t.Errorf("after edit it's for %v, want nobody", ids)
	}
	expectRedirect(t, phone.post("/admin/countdowns/"+itoa(spain.ID)+"/delete", nil), "/admin/countdowns?flash=Deleted+Spain%21.")
	expect(t, phone.get("/admin/countdowns/"+itoa(spain.ID)), http.StatusNotFound)
	expect(t, phone.get("/admin"), http.StatusOK, "added a countdown", "removed a countdown")

	// A passed one-off goes to Passed in admin and off the board.
	h.advance(10 * 24 * 3600e9) // Sat 11 Jul
	list = phone.get("/admin/countdowns")
	expect(t, list, http.StatusOK, "Passed (1)", "Sports day")
	notContains(t, wall.get("/board/countdowns"), "Sports day")

	// Only adults manage them.
	expectRedirect(t, wall.get("/admin/countdowns"), "/admin/unlock")
}

func TestCountdownsModuleOff(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Sports day"}, "emoji": {"⚽"}, "on_date": {"2026-07-10"},
	}), "/admin/countdowns?flash=Added+Sports+day.")
	expect(t, setModules(t, phone, "rewards", "money", "photos", "calendar"), http.StatusOK, "Saved.")
	expect(t, phone.get("/admin/countdowns"), http.StatusNotFound)
	expect(t, wall.get("/board/countdowns"), http.StatusNotFound)
	notContains(t, wall.get("/board"), "countdown-strip")
	notContains(t, wall.get("/"), `data-panel="countdowns"`)
	notContains(t, wall.get("/screensaver"), "Sports day")
	notContains(t, phone.get("/admin/people/"+itoa(h.person("Alfie").ID)), "birth_day")
	notContains(t, phone.get("/admin"), `href="/admin/countdowns"`)
}

func TestKidAddsCountdown(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, isla := itoa(h.person("Alfie").ID), itoa(h.person("Isla").ID)
	expect(t, phone.post("/admin/people/"+alfie+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}), http.StatusOK, "PIN saved.")

	// Without the tick, nobody can.
	expect(t, wall.get("/board/countdowns/by/"+alfie), http.StatusForbidden)
	expect(t, wall.post("/board/countdowns/by/"+alfie, url.Values{"title": {"Party"}, "on_date": {"2026-07-20"}}), http.StatusForbidden)

	// A grown-up ticks Can add countdowns for Alfie.
	expect(t, phone.post("/admin/people/"+alfie, url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"},
		"countdowns_shown": {"1"}, "can_add_countdowns": {"1"},
	}), http.StatusOK, "Saved.", `name="can_add_countdowns" value="1" checked`)
	expect(t, wall.get("/board/countdowns"), http.StatusOK, "+ Add a countdown")
	who := wall.get("/board/countdowns/add")
	expect(t, who, http.StatusOK, "Who's adding it?", `hx-get="/board/countdowns/by/`+alfie+`"`)
	notContains(t, who, `/board/countdowns/by/`+isla+`"`)
	expect(t, wall.get("/board/countdowns/by/"+isla), http.StatusForbidden)

	// The PIN pad, a wrong PIN, then the right one.
	expect(t, wall.get("/board/countdowns/by/"+alfie), http.StatusOK, "Alfie's countdowns", "Enter your PIN", "data-pinpad")
	expect(t, wall.post("/board/countdowns/by/"+alfie+"/pin", url.Values{"pin": {"0000"}}), http.StatusUnauthorized, "Wrong PIN")
	r := wall.post("/board/countdowns/by/"+alfie+"/pin", url.Values{"pin": {"1357"}})
	expect(t, r, http.StatusOK, "Add a countdown", `name="title"`, `name="on_date"`)
	notContains(t, r, `name="people_ids"`)
	m := countdownToken.FindStringSubmatch(r.body)
	if m == nil {
		t.Fatalf("no token in\n%s", r.body)
	}
	q := "?t=" + m[1]

	// Without the token it asks for the PIN again.
	expect(t, wall.post("/board/countdowns/by/"+alfie, url.Values{"title": {"Party"}, "on_date": {"2026-07-20"}}), http.StatusOK, "Enter your PIN again")
	if n := len(must(h.st.Q.ListCountdowns(h.ctx(), h.hh))); n != 0 {
		t.Fatalf("%d countdowns saved without a PIN", n)
	}
	expect(t, wall.post("/board/countdowns/by/"+alfie+q, url.Values{"title": {""}, "on_date": {"2026-07-20"}}), http.StatusUnprocessableEntity, "Give it a name")
	r = wall.post("/board/countdowns/by/"+alfie+q, url.Values{"title": {"My party"}, "emoji": {"🎉"}, "on_date": {"2026-07-20"}})
	expect(t, r, http.StatusOK, "Added My party.", "Yours", "My party", "2 weeks")
	if !strings.Contains(r.header.Get("HX-Trigger"), "ingle:countdowns") {
		t.Errorf("HX-Trigger = %q", r.header.Get("HX-Trigger"))
	}
	party := countdownNamed(t, h, "My party")
	if party.AddedBy == nil || itoa(*party.AddedBy) != alfie {
		t.Errorf("added by %v, want Alfie", party.AddedBy)
	}
	expect(t, wall.get("/board/countdowns"), http.StatusOK, "My party")

	// Change it, then delete it.
	expect(t, wall.get("/board/countdowns/by/"+alfie+"/edit/"+itoa(party.ID)+q), http.StatusOK, "Change My party", `value="2026-07-20"`)
	expect(t, wall.post("/board/countdowns/by/"+alfie+"/edit/"+itoa(party.ID)+q, url.Values{"title": {"Big party"}, "on_date": {"2026-07-21"}}),
		http.StatusOK, "Saved Big party.")

	// Not a grown-up's countdown, nor anyone else's.
	expectRedirect(t, phone.post("/admin/countdowns", url.Values{
		"title": {"Sports day"}, "emoji": {"⚽"}, "on_date": {"2026-07-10"},
	}), "/admin/countdowns?flash=Added+Sports+day.")
	sports := countdownNamed(t, h, "Sports day")
	expect(t, wall.post("/board/countdowns/by/"+alfie+"/delete/"+itoa(sports.ID)+q, nil), http.StatusForbidden)
	expect(t, wall.post("/board/countdowns/by/"+alfie+"/edit/"+itoa(sports.ID)+q, url.Values{"title": {"Mine now"}, "on_date": {"2026-07-10"}}), http.StatusForbidden)

	expect(t, wall.post("/board/countdowns/by/"+alfie+"/delete/"+itoa(party.ID)+q, nil), http.StatusOK, "Deleted Big party.")
	if n := len(must(h.st.Q.ListCountdowns(h.ctx(), h.hh))); n != 1 {
		t.Errorf("%d countdowns left, want Sports day", n)
	}

	// Untick: the token no longer lets them.
	expect(t, phone.post("/admin/people/"+alfie, url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"},
		"countdowns_shown": {"1"},
	}), http.StatusOK, "Saved.")
	expect(t, wall.post("/board/countdowns/by/"+alfie+q, url.Values{"title": {"Again"}, "on_date": {"2026-07-20"}}), http.StatusForbidden)
}

func countdownNamed(t *testing.T, h *harness, title string) dbq.Countdown {
	t.Helper()
	for _, c := range must(h.st.Q.ListCountdowns(h.ctx(), h.hh)) {
		if c.Title == title {
			return c
		}
	}
	t.Fatalf("no countdown %q", title)
	return dbq.Countdown{}
}
