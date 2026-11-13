package server

import (
	"html"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// calICS is a calendar with one event.
func calICS(uid, start, end, title, extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART;TZID=Europe/London:" + start + "\r\nDTEND;TZID=Europe/London:" + end +
		"\r\nSUMMARY:" + title + "\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// The board calendar filters to one person's calendars, hides calendars,
// and says which calendar each event is from.
func TestCalendarFilters(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, isla := h.person("Alfie"), h.person("Isla")
	add := func(name, owner, ics string) int64 {
		t.Helper()
		h.setICS(ics)
		expect(t, phone.post("/admin/calendars", url.Values{"name": {name}, "url": {h.ics.URL + "/" + name + ".ics"}, "person": {owner}}),
			http.StatusOK, name+" added.")
		feeds := must(h.st.Q.ListCalendarFeeds(h.ctx(), h.hh))
		for _, f := range feeds {
			if f.Name == name {
				return f.ID
			}
		}
		t.Fatalf("no feed %s", name)
		return 0
	}
	family := add("Family", "", calICS("1", "20260702T090000", "20260702T100000", "Swimming", "LOCATION:Leisure centre\r\n"))
	club := add("Club", itoa(alfie.ID), calICS("2", "20260702T160000", "20260702T170000", "Football", ""))
	add("Dance", itoa(isla.ID), calICS("3", "20260703T170000", "20260703T180000", "Ballet", ""))
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Tidy room"}, "mode": {"assigned"}, "assignee_id": {itoa(isla.ID)}, "repeat": {"once"},
		"starts_on": {"2026-07-01"}, "points": {"1"},
	}), "/admin/chores")
	expect(t, phone.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-04"}, "holiday_to": {"2026-07-04"}, "holiday_person": {itoa(isla.ID)}}),
		http.StatusOK, "Holiday added.")

	// Everyone: a chip for each person with a calendar (not Mum), the
	// Calendars button, and each event's calendar.
	r := wall.get("/board/calendar?chores=1")
	expect(t, r, http.StatusOK, "Swimming", "Football", "Ballet", "Feed the cat", "Tidy room", "Hoover the lounge", "🏖️ Isla away",
		`hx-get="/board/calendar?chores=1&amp;from=2026-06-29&amp;person=`+itoa(alfie.ID)+`&amp;view=week"`,
		` Alfie</button>`, ` Isla</button>`, `aria-pressed="false">Calendars</button>`,
		`<span class="cal-item__feed">Family</span>`, `<span class="cal-item__feed">Club</span>`)
	notContains(t, r, ` Mum</button>`)

	// Alfie: his calendar and the family's, his chores and ones up for
	// grabs, everyone's holidays. The URLs keep him, and his chip goes
	// back to everyone.
	r = wall.get("/board/calendar?chores=1&person=" + itoa(alfie.ID))
	expect(t, r, http.StatusOK, "Swimming", "Football", "Feed the cat", "Hoover the lounge",
		`data-src="/board/calendar?chores=1&amp;from=2026-06-29&amp;person=`+itoa(alfie.ID)+`&amp;view=week"`,
		`hx-get="/board/calendar?chores=1&amp;from=2026-07-06&amp;person=`+itoa(alfie.ID)+`&amp;view=week"`,
		`hx-get="/board/calendar?chores=1&amp;from=2026-06-29&amp;view=week" hx-target="#calendar" hx-swap="innerHTML" aria-pressed="true"`)
	notContains(t, r, "Ballet", "Tidy room", "Isla away")
	// Isla's own holiday shows for her.
	r = wall.get("/board/calendar?chores=1&person=" + itoa(isla.ID))
	expect(t, r, http.StatusOK, "Swimming", "Ballet", "Tidy room", "Hoover the lounge", "Isla away")
	notContains(t, r, "Football", "Feed the cat")

	// The month's day sheets keep the filter.
	expect(t, wall.get("/board/calendar?view=month&person="+itoa(isla.ID)), http.StatusOK,
		`hx-get="/board/calendar/day/2026-07-02?chores=0&amp;person=`+itoa(isla.ID)+`"`)
	r = wall.get("/board/calendar/day/2026-07-02?chores=0&person=" + itoa(isla.ID))
	expect(t, r, http.StatusOK, "Swimming")
	notContains(t, r, "Football")

	// Hiding a calendar, by the URL or the Calendars sheet.
	r = wall.get("/board/calendar?hide=" + itoa(family))
	expect(t, r, http.StatusOK, "Football", `aria-pressed="true">Calendars · 1 hidden</button>`,
		`data-src="/board/calendar?chores=0&amp;from=2026-06-29&amp;hide=`+itoa(family)+`&amp;view=week"`)
	notContains(t, r, "Swimming")
	r = wall.get("/board/calendar/calendars?view=week&from=2026-06-29&chores=0&person=" + itoa(alfie.ID) + "&hide=" + itoa(family))
	expect(t, r, http.StatusOK, `hx-get="/board/calendar"`, `name="person" value="`+itoa(alfie.ID)+`"`, `name="pick" value="1"`,
		`name="show" value="`+itoa(family)+`">`, `name="show" value="`+itoa(club)+`" checked>`, "Family</span>", " Alfie</span>")
	r = wall.get("/board/calendar?view=week&from=2026-06-29&chores=0&pick=1&show=" + itoa(club))
	expect(t, r, http.StatusOK, "Football", "Calendars · 2 hidden")
	notContains(t, r, "Swimming", "Ballet")
	// An unknown calendar id is ignored.
	expect(t, wall.get("/board/calendar?hide=999999"), http.StatusOK, "Swimming", `aria-pressed="false">Calendars</button>`)

	// The event sheet: when, where, which calendar and whose.
	london, _ := time.LoadLocation("Europe/London")
	sheet := func(title string, feed int64, at time.Time) string {
		return "/board/calendar/event?" + url.Values{"feed": {itoa(feed)}, "start": {itoa(at.Unix())}, "title": {title}}.Encode()
	}
	swim := sheet("Swimming", family, time.Date(2026, 7, 2, 9, 0, 0, 0, london))
	expect(t, wall.get("/board/calendar"), http.StatusOK, `hx-get="`+html.EscapeString(swim)+`"`)
	expect(t, wall.get(swim), http.StatusOK, "<dd>Thursday 2 July, 09:00–10:00</dd>", "<dd>Leisure centre</dd>",
		"Family</span></dd>", "<dt>Whose</dt><dd>Family</dd>")
	expect(t, wall.get(sheet("Football", club, time.Date(2026, 7, 2, 16, 0, 0, 0, london))), http.StatusOK,
		"Club</span></dd>", " Alfie</dd>")
	r = wall.get(sheet("Football", family, time.Date(2026, 7, 2, 16, 0, 0, 0, london)))
	expect(t, r, http.StatusConflict)
	if msg := toastOf(t, r).Message; msg != "That event isn't on the calendar any more." {
		t.Errorf("toast = %q", msg)
	}
	expect(t, wall.get("/board/calendar/event?feed=x"), http.StatusNotFound)
}
