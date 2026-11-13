package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// It's 00:30 BST on Wednesday 1 July 2026; the week is Mon 29 Jun – Sun 5 Jul.
func TestBoardCalendar(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Bins out"}, "emoji": {"🗑️"}, "mode": {"pool"}, "repeat": {"weekly"}, "days": {"1"},
		"starts_on": {"2026-06-29"}, "points": {"3"},
	}), "/admin/chores")

	// The board has the view dots and a calendar panel.
	expect(t, wall.get("/"), http.StatusOK, `data-view-to="calendar"`, `id="calendar"`, "Chores", "Calendar")

	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Europe/London:20260702T090000\r\nDTEND;TZID=Europe/London:20260702T100000\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:2\r\nDTSTART;VALUE=DATE:20260703\r\nDTEND;VALUE=DATE:20260705\r\nSUMMARY:Camping\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {h.ics.URL + "/secret-token/basic.ics"}}),
		http.StatusOK, "Family added.")

	// Week view, this week.
	r := wall.get("/board/calendar?chores=1")
	expect(t, r, http.StatusOK, "29 Jun – 5 Jul", `aria-current="date"`, "Wed 1 Jul",
		"Feed the cat", "08:00", "Alfie", "Hoover the lounge", "Up for grabs",
		"09:00</span> <span class=\"cal-item__title\">Swimming", "Camping",
		`hx-get="/board/calendar/chores/`+itoa(h.chore("Feed the cat").ID)+`/2026-07-01?person=`+itoa(h.person("Alfie").ID)+`&amp;at=08:00"`,
		`style="--item: #46A758"`)
	if n := strings.Count(r.body, ">Camping<"); n != 2 {
		t.Errorf("two-day event shows %d times, want 2", n)
	}
	if strings.Contains(r.body, "hx-post") || strings.Contains(r.body, "<form") {
		t.Error("calendar has something that changes data")
	}
	if strings.Contains(r.body, "Bins out") {
		t.Error("a weekly chore shows on a Monday before it was set up")
	}
	if strings.Contains(r.body, "Add calendar feeds") {
		t.Error("shows the no-feeds hint with a feed")
	}

	// Projected chores: three weeks ahead is past the materialised window.
	r = wall.get("/board/calendar?chores=1&view=week&from=2026-07-22")
	expect(t, r, http.StatusOK, "20–26 Jul", "Feed the cat", "🗑️", "Bins out", "Swimming")
	if strings.Contains(r.body, "Hoover the lounge") {
		t.Error("a one-off chore repeats")
	}
	// Snapped to the week start, and clamped at both ends.
	expect(t, wall.get("/board/calendar?from=2026-07-08"), http.StatusOK, "6–12 Jul")
	r = wall.get("/board/calendar?from=2020-01-01")
	expect(t, r, http.StatusOK, "1–7 Jun", `aria-label="Previous week"`+"\n              disabled")
	r = wall.get("/board/calendar?from=2030-01-01")
	expect(t, r, http.StatusOK, "28 Sep – 4 Oct", `aria-label="Next week"`+"\n              disabled")

	// Past days: done chores ticked, missed ones marked.
	expect(t, wall.post("/board/occurrences/"+itoa(h.occurrence("Feed the cat").ID)+"/complete", nil), http.StatusOK)
	h.advance(48 * 60 * 60 * 1e9)
	r = wall.get("/board/calendar?chores=1")
	day := func(body, label string) string {
		i := strings.Index(body, `aria-label="`+label+`"`)
		if i < 0 {
			return ""
		}
		rest := body[i:]
		if j := strings.Index(rest[1:], "<section"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	expect(t, resp{status: 200, body: day(r.body, "Wed 1 Jul")}, http.StatusOK, "cal-item--done", "Done by Alfie")
	expect(t, resp{status: 200, body: day(r.body, "Thu 2 Jul")}, http.StatusOK, "cal-item--missed", "Missed")

	// Month view.
	r = wall.get("/board/calendar?chores=1&view=month")
	expect(t, r, http.StatusOK, "July 2026", `class="cal__month"`, "cal__cell--outside", `hx-get="/board/calendar/day/2026-07-09?chores=1"`, "Swimming")
	expect(t, wall.get("/board/calendar?view=month&from=2027-05-01"), http.StatusOK, "September 2026")
	expect(t, wall.get("/board/calendar?view=month&from=2020-05-01"), http.StatusOK, "June 2026")

	// Day sheet and chore sheet: read-only.
	r = wall.get("/board/calendar/day/2026-07-09?chores=1")
	expect(t, r, http.StatusOK, "Thu 9 Jul", "Swimming", "Feed the cat", "data-close")
	r = wall.get("/board/calendar/chores/" + itoa(h.chore("Bins out").ID) + "/2026-07-20")
	expect(t, r, http.StatusOK, "Bins out", "Monday 20 July", "Up for grabs", "Weekly: Mon", "Coming up", "Worth")
	if strings.Contains(r.body, "hx-post") {
		t.Error("chore sheet can change things")
	}
	r = wall.get("/board/calendar/chores/" + itoa(h.chore("Feed the cat").ID) + "/2026-07-01")
	expect(t, r, http.StatusOK, "Done by Alfie at")
	// Not due that day.
	r = wall.get("/board/calendar/chores/" + itoa(h.chore("Bins out").ID) + "/2026-07-21")
	if r.status != http.StatusConflict || !strings.Contains(toastOf(t, r).Message, "isn't on the calendar") {
		t.Errorf("chore not due that day = %d %q", r.status, r.header.Get("HX-Trigger"))
	}
	if r := wall.get("/board/calendar/day/1999-01-01"); r.status != http.StatusNotFound {
		t.Errorf("far-off day = %d, want 404", r.status)
	}
}

// The calendar shows events (and holidays) by default; the household's
// setting or the board's Chores button adds the chores.
func TestCalendarChores(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Europe/London:20260702T090000\r\nDTEND;TZID=Europe/London:20260702T100000\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {h.ics.URL + "/basic.ics"}}),
		http.StatusOK, "Family added.")
	expect(t, phone.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-03"}, "holiday_to": {"2026-07-03"}, "holiday_person": {""}}),
		http.StatusOK, "Holiday added.")

	// Off by default: events and holidays, no chores. The URLs keep it off.
	r := wall.get("/board/calendar")
	expect(t, r, http.StatusOK, "Swimming", "🏖️ Holiday",
		`data-src="/board/calendar?chores=0&amp;from=2026-06-29&amp;view=week"`,
		`hx-get="/board/calendar?chores=1&amp;from=2026-06-29&amp;view=week" hx-target="#calendar" hx-swap="innerHTML" aria-pressed="false">Chores`,
		`hx-get="/board/calendar?chores=0&amp;from=2026-07-06&amp;view=week"`)
	notContains(t, r, "Feed the cat", "Hoover the lounge")
	r = wall.get("/board/calendar?view=month")
	expect(t, r, http.StatusOK, "Swimming", `hx-get="/board/calendar/day/2026-07-02?chores=0"`)
	notContains(t, r, "Feed the cat")
	r = wall.get("/board/calendar/day/2026-07-02?chores=0")
	expect(t, r, http.StatusOK, "Swimming")
	notContains(t, r, "Feed the cat")

	// The Chores button turns them on, and browsing keeps them.
	r = wall.get("/board/calendar?chores=1&from=2026-06-29&view=week")
	expect(t, r, http.StatusOK, "Swimming", "Feed the cat",
		`aria-pressed="true">Chores`, `hx-get="/board/calendar?chores=0&amp;from=2026-06-29&amp;view=week"`,
		`hx-get="/board/calendar?chores=1&amp;from=2026-06-29&amp;view=month"`)
	expect(t, wall.get("/board/calendar/day/2026-07-02?chores=1"), http.StatusOK, "Swimming", "Feed the cat")

	// The setting makes chores the default; the button still hides them.
	form := url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"5"}, "calendar_idle_minutes": {"2"},
		"lock_idle_hours": {"0"}, "lock_from": {"09:00"}, "lock_until": {"15:00"}, "calendar_chores": {"1"},
	}
	expect(t, phone.post("/admin/settings", form), http.StatusOK, "Settings saved.", `name="calendar_chores" value="1" checked`)
	expect(t, wall.get("/board/calendar"), http.StatusOK, "Feed the cat", `aria-pressed="true">Chores`)
	expect(t, wall.get("/board/calendar/day/2026-07-02"), http.StatusOK, "Feed the cat")
	notContains(t, wall.get("/board/calendar?chores=0"), "Feed the cat")
	form.Del("calendar_chores")
	expect(t, phone.post("/admin/settings", form), http.StatusOK, "Settings saved.")
	notContains(t, phone.get("/admin/settings"), `name="calendar_chores" value="1" checked`)
	notContains(t, wall.get("/board/calendar"), "Feed the cat")
}

func TestBoardCalendarLocked(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp() // a display unlocked as Mum
	expectRedirect(t, wall.post("/lock", nil), "/")
	for _, p := range []string{"/board/calendar", "/board/calendar/day/2026-07-01", "/board/calendar/chores/1/2026-07-01"} {
		if r := wall.get(p); r.status != http.StatusLocked {
			t.Errorf("GET %s while locked = %d, want 423", p, r.status)
		}
	}
	if r := wall.get("/"); strings.Contains(r.body, "data-view-to") || strings.Contains(r.body, `id="calendar"`) {
		t.Error("locked display offers the calendar")
	}
}

func TestCalendarIdleSetting(t *testing.T) {
	_, wall, phone := boardHousehold(t)
	expect(t, wall.get("/"), http.StatusOK, "calIdleMs =  120  * 1000")
	expect(t, phone.get("/admin/settings"), http.StatusOK, `name="calendar_idle_minutes" value="2"`)
	form := url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"5"}, "lock_idle_hours": {"0"},
		"lock_from": {"09:00"}, "lock_until": {"15:00"}, "calendar_idle_minutes": {"61"},
	}
	expect(t, phone.post("/admin/settings", form), http.StatusUnprocessableEntity, "Choose between 0 (never) and 60 minutes.")
	form.Set("calendar_idle_minutes", "0")
	expect(t, phone.post("/admin/settings", form), http.StatusOK, "Settings saved.")
	if r := wall.get("/"); strings.Contains(r.body, "120  * 1000") {
		t.Error("calendar idle still 2 minutes after setting 0")
	}
}
