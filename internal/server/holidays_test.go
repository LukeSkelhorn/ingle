package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// holidayIDs lists household hh's holidays, soonest first.
func (h *harness) holidayIDs(hh int64) []int64 {
	h.t.Helper()
	var ids []int64
	for _, x := range must(h.st.Q.ListHolidays(store.WithHousehold(h.t.Context(), hh), dbq.ListHolidaysParams{HouseholdID: hh, FromDate: "2000-01-01"})) {
		ids = append(ids, x.ID)
	}
	return ids
}

// The harness's board day is Wed 1 Jul 2026.
func TestHolidayMode(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expect(t, phone.get("/admin/settings"), http.StatusOK, "Holiday mode", "No holidays planned.", `value="2026-07-01"`)

	expect(t, phone.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-03"}, "holiday_to": {"2026-07-02"}}),
		http.StatusUnprocessableEntity, "The last day can&#39;t be before the first.")

	// Everyone away until Sunday: the board's empty, with the banner.
	expect(t, phone.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-01"}, "holiday_to": {"2026-07-05"}, "holiday_person": {""}}),
		http.StatusOK, "Holiday added.", "Everyone", "Wed 1 Jul – Sun 5 Jul", "On now", "End now")
	r := wall.get("/")
	expect(t, r, http.StatusOK, "🏖️ On holiday until Sunday")
	if strings.Contains(section(r.body, "Alfie"), "Feed the cat") || strings.Contains(r.body, "Up for grabs <span") {
		t.Errorf("board shows chores on holiday\n%s", section(r.body, "Alfie"))
	}
	expect(t, wall.get("/board"), http.StatusOK, "🏖️ On holiday until Sunday")

	// End now (it started today, so it's removed): the chores are back.
	ids := h.holidayIDs(h.hh)
	expect(t, phone.post("/admin/settings/holidays/"+itoa(ids[0])+"/end", url.Values{}), http.StatusOK, "Holiday ended.", "No holidays planned.")
	r = wall.get("/")
	expect(t, r, http.StatusOK, "Feed the cat", "Up for grabs <span")
	expectNot(t, r, "On holiday")

	// Just Alfie, until Friday.
	alfie := h.person("Alfie")
	expect(t, phone.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-01"}, "holiday_to": {"2026-07-03"}, "holiday_person": {itoa(alfie.ID)}}),
		http.StatusOK, "Alfie</strong> · Wed 1 Jul – Fri 3 Jul")
	r = wall.get("/")
	if col := section(r.body, "Alfie"); !strings.Contains(col, "🏖️ Away until Friday") || strings.Contains(col, "Feed the cat") {
		t.Errorf("Alfie's column on holiday:\n%s", col)
	}
	expectNot(t, r, "On holiday")
	ids = h.holidayIDs(h.hh)
	expect(t, phone.post("/admin/settings/holidays/"+itoa(ids[0])+"/delete", url.Values{}), http.StatusOK, "Holiday removed.")
	expect(t, wall.get("/"), http.StatusOK, "Feed the cat")

	expect(t, phone.post("/admin/settings/holidays/999999/delete", url.Values{}), http.StatusNotFound)
}

func TestTenancyHolidays(t *testing.T) {
	x := newTwoHouseholds(t)
	// A can't send B's child on holiday…
	expect(t, x.aW.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-01"}, "holiday_to": {"2026-07-02"}, "holiday_person": {itoa(x.beaID)}}),
		http.StatusUnprocessableEntity, "Choose someone in the household.")
	// …or end or remove B's holiday.
	expect(t, x.bW.post("/admin/settings/holidays", url.Values{"holiday_from": {"2026-07-01"}, "holiday_to": {"2026-07-02"}}), http.StatusOK, "Holiday added.")
	id := x.h.holidayIDs(x.b)[0]
	expect(t, x.aW.post("/admin/settings/holidays/"+itoa(id)+"/end", url.Values{}), http.StatusNotFound)
	expect(t, x.aW.post("/admin/settings/holidays/"+itoa(id)+"/delete", url.Values{}), http.StatusNotFound)
	if len(x.h.holidayIDs(x.b)) != 1 {
		t.Error("A changed B's holiday")
	}
	expect(t, x.bW.get("/board"), http.StatusOK, "On holiday")
	expectNot(t, x.aW.get("/board"), "On holiday")
}
