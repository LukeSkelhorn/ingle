package server

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func TestKeepHistorySetting(t *testing.T) {
	h, _, phone := boardHousehold(t)
	form := url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"5"}, "calendar_idle_minutes": {"2"},
		"lock_idle_hours": {"0"}, "lock_from": {"09:00"}, "lock_until": {"15:00"},
	}

	// Everything to start with; the page knows what's saved, to ask first.
	expect(t, phone.get("/admin/settings"), http.StatusOK, "Keep history",
		`<select name="keep_history_months" data-keep-history="0">`, `<option value="0" selected>Everything</option>`,
		"cleared out each night, after the backup")

	form.Set("keep_history_months", "12")
	expect(t, phone.post("/admin/settings", form), http.StatusOK, "Settings saved.",
		`data-keep-history="12"`, `<option value="12" selected>12 months</option>`)
	if set, err := h.srv.Settings.Get(h.ctx(), h.hh); err != nil || set.KeepHistoryMonths != 12 {
		t.Fatalf("saved %d, %v; want 12", set.KeepHistoryMonths, err)
	}

	// Only Everything, 24 or 12 months; a bad one keeps what's saved.
	form.Set("keep_history_months", "6")
	expect(t, phone.post("/admin/settings", form), http.StatusUnprocessableEntity,
		"Choose how much history to keep.", `data-keep-history="12"`)
	form.Set("keep_history_months", "lots")
	expect(t, phone.post("/admin/settings", form), http.StatusUnprocessableEntity, "Choose how much history to keep.")

	// With backups off (SQLite), nothing is cleared, so it doesn't ask.
	h.srv.NoClearOut = true
	expect(t, phone.get("/admin/settings"), http.StatusOK, "Backups are off, so nothing is cleared out.",
		`<select name="keep_history_months">`)
}

func TestBroughtForwardInMyMoney(t *testing.T) {
	h, _, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 40, Pence: 1250, Kind: "brought_forward",
		CreatedAt: h.clock().AddDate(0, 0, -3).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	expect(t, phone.get("/admin/people/"+itoa(alfie.ID)+"/money"), http.StatusOK, "Brought forward", "£12.50")
}
