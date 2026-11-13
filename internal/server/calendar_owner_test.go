package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// A calendar can be one person's: their events show in their colour, with
// their avatar, on the board calendar and the screensaver.
func TestCalendarOwner(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, isla := h.person("Alfie"), h.person("Isla")
	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Europe/London:20260701T090000\r\nDTEND;TZID=Europe/London:20260701T100000\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")

	// The add form offers the family and each person; the owner is checked.
	expect(t, phone.get("/admin/calendars"), http.StatusOK, "Whose is it?", `<option value="">The family</option>`,
		`<option value="`+itoa(alfie.ID)+`">Alfie</option>`)
	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Swim club"}, "url": {h.ics.URL + "/a.ics"}, "person": {"999999"}}),
		http.StatusUnprocessableEntity, "Choose someone in the household, or the family.")
	r := phone.post("/admin/calendars", url.Values{"name": {"Swim club"}, "url": {h.ics.URL + "/a.ics"}, "person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusOK, "Swim club added.", "Alfie's ·")

	// Alfie's colour and avatar on the board calendar and screensaver.
	expect(t, wall.get("/board/calendar"), http.StatusOK,
		`style="--item: `+alfie.Colour+`"><span class="cal-item__when">09:00</span> <span class="cal-item__title">Swimming</span> <span class="cal-item__who" title="Alfie's calendar">`)
	expect(t, wall.get("/screensaver"), http.StatusOK,
		`<li class="saver__event"><span class="avatar avatar--xs" style="--person: `+alfie.Colour+`"`)

	id := regexp.MustCompile(`/admin/calendars/(\d+)/edit`).FindStringSubmatch(phone.get("/admin/calendars").body)
	if id == nil {
		t.Fatal("no edit form")
	}
	edit := "/admin/calendars/" + id[1] + "/edit"

	// A bad edit shows that form open, with what was typed.
	r = phone.post(edit, url.Values{"name": {""}, "person": {itoa(isla.ID)}})
	expect(t, r, http.StatusUnprocessableEntity, `<details class="feed-edit" open>`, "Enter a name (up to 50 characters).",
		`<option value="`+itoa(isla.ID)+`" selected>Isla</option>`)

	// Isla's now, then the family's.
	expect(t, phone.post(edit, url.Values{"name": {"Swimming"}, "person": {itoa(isla.ID)}}), http.StatusOK, "Swimming saved.", "Isla's ·")
	expect(t, wall.get("/board/calendar"), http.StatusOK, `style="--item: `+isla.Colour+`"><span class="cal-item__when">09:00`)
	r = phone.post(edit, url.Values{"name": {"Swimming"}, "person": {""}})
	expect(t, r, http.StatusOK, "Swimming saved.", "Family ·")
	r = wall.get("/board/calendar")
	if strings.Contains(r.body, "Isla's calendar") || strings.Contains(r.body, `style="--item: `+isla.Colour+`"><span class="cal-item__when">09:00`) {
		t.Error("a family event still shows Isla")
	}
	expectNot(t, wall.get("/screensaver"), `<li class="saver__event"><span class="avatar`)
	expect(t, phone.get("/admin"), http.StatusOK, "edited a calendar")

	// Archiving Isla hands her calendar back to the family.
	expect(t, phone.post(edit, url.Values{"name": {"Swimming"}, "person": {itoa(isla.ID)}}), http.StatusOK, "Swimming saved.")
	expectRedirect(t, phone.post("/admin/people/"+itoa(isla.ID)+"/archive", nil), "/admin/people")
	expect(t, phone.get("/admin/calendars"), http.StatusOK, "Family ·")
	expectNot(t, wall.get("/board/calendar"), "Isla's calendar")

	expect(t, phone.post("/admin/calendars/999999/edit", url.Values{"name": {"X"}}), http.StatusNotFound, "That calendar has gone.")
}

func TestTenancyCalendarOwner(t *testing.T) {
	x := newTwoHouseholds(t)
	x.h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nEND:VCALENDAR\r\n")
	// A can't give a calendar to B's person…
	expect(t, x.aW.post("/admin/calendars", url.Values{"name": {"Work"}, "url": {x.h.ics.URL + "/a.ics"}, "person": {itoa(x.beaID)}}),
		http.StatusUnprocessableEntity, "Choose someone in the household, or the family.")
	// …or edit B's calendar.
	expect(t, x.bW.post("/admin/calendars", url.Values{"name": {"Work"}, "url": {x.h.ics.URL + "/b.ics"}}), http.StatusOK, "Work added.")
	id := regexp.MustCompile(`/admin/calendars/(\d+)/edit`).FindStringSubmatch(x.bW.get("/admin/calendars").body)
	if id == nil {
		t.Fatal("no edit form")
	}
	expect(t, x.aW.post("/admin/calendars/"+id[1]+"/edit", url.Values{"name": {"Mine now"}}), http.StatusNotFound)
	expect(t, x.bW.get("/admin/calendars"), http.StatusOK, "<strong>Work</strong>")
}
