package server

import (
	"encoding/hex"
	"net/http"
	"slices"
	"strings"

	"github.com/lukeskelhorn/domus/internal/settings"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

type adminHome struct {
	People   int
	Chores   int
	Rewards  int
	Pending  int
	Activity []dbq.ListRecentActivityWithActorRow
}

func (s *Server) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	people, err := s.Household.People(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	chs, err := s.Chores.Chores(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rewards, err := s.Household.Rewards(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pending, err := s.Store.Q.ListPendingCompletions(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	act, err := s.Store.Q.ListRecentActivityWithActor(ctx, dbq.ListRecentActivityWithActorParams{HouseholdID: s.HouseholdID, Limit: 20})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.view(r, "Admin", adminHome{
		People: len(people), Chores: len(chs), Rewards: len(rewards), Pending: len(pending), Activity: act,
	})
	v.Nav = "home"
	s.render(w, r, http.StatusOK, "admin_home", v)
}

// describeActivity turns an activity verb into a short phrase for the feed.
func describeActivity(verb string) string {
	if d, ok := activityText[verb]; ok {
		return d
	}
	return strings.ReplaceAll(verb, ".", " ")
}

var activityText = map[string]string{
	"household.setup":      "set up the household",
	"settings.updated":     "changed settings",
	"admin.unlocked":       "unlocked admin on a display",
	"admin.signed_in":      "signed in",
	"device.registered":    "registered a household display",
	"device.revoked":       "removed a household display",
	"session.revoked":      "signed out a device",
	"person.created":       "added a family member",
	"person.updated":       "edited a family member",
	"person.archived":      "archived a family member",
	"person.restored":      "restored a family member",
	"person.pin_set":       "set a PIN",
	"person.pin_cleared":   "removed a PIN",
	"person.login_set":     "set a sign-in",
	"person.login_cleared": "removed a sign-in",
	"chore.created":        "added a chore",
	"chore.updated":        "edited a chore",
	"chore.archived":       "archived a chore",
	"chore.claimed":        "claimed a chore",
	"chore.released":       "gave back a chore",
	"chore.completed":      "completed a chore",
	"chore.undone":         "undid a chore",
	"chore.approved":       "approved a chore",
	"chore.rejected":       "sent a chore back",
	"reward.created":       "added a reward",
	"reward.updated":       "edited a reward",
	"reward.archived":      "archived a reward",
	"reward.restored":      "restored a reward",
	"reward.redeemed":      "redeemed a reward",
}

// Settings.

type settingsForm struct {
	HouseholdName    string
	Timezone         string
	DayStartsAt      string
	AdminIdleMinutes int64
	Timezones        []string
}

func (s *Server) settingsForm(set settings.Settings) settingsForm {
	return settingsForm{
		HouseholdName:    set.HouseholdName,
		Timezone:         set.Location.String(),
		DayStartsAt:      set.DayStartsAt,
		AdminIdleMinutes: set.AdminIdleMinutes,
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Settings.Get(r.Context(), s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderSettings(w, r, http.StatusOK, s.settingsForm(set), nil, "")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, f settingsForm, errs map[string]string, flash string) {
	f.Timezones = timezoneOptions(f.Timezone)
	v := s.view(r, "Settings", f)
	v.Nav = "settings"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_settings", v)
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	data := settingsForm{
		HouseholdName:    f.str("household_name"),
		Timezone:         f.str("timezone"),
		DayStartsAt:      f.str("day_starts_at"),
		AdminIdleMinutes: f.int("admin_idle_minutes", "Choose between 1 and 60 minutes."),
	}
	if !f.ok() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, data, f.errs, "")
		return
	}
	err := s.Settings.Update(r.Context(), s.HouseholdID, actor(r).Person.ID, settings.Input{
		HouseholdName: data.HouseholdName, Timezone: data.Timezone,
		DayStartsAt: data.DayStartsAt, AdminIdleMinutes: data.AdminIdleMinutes,
	})
	if errs, ok := validationErrors(err); ok {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, data, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderSettings(w, r, http.StatusOK, data, nil, "Settings saved.")
}

// commonTimezones is the curated timezone list (the IANA database embedded
// in Go can't be enumerated). UK first; the current value is always added.
var commonTimezones = []string{
	"Europe/London", "Europe/Dublin", "Europe/Lisbon", "Europe/Paris", "Europe/Brussels",
	"Europe/Amsterdam", "Europe/Berlin", "Europe/Madrid", "Europe/Rome", "Europe/Zurich",
	"Europe/Vienna", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen", "Europe/Helsinki",
	"Europe/Warsaw", "Europe/Prague", "Europe/Athens", "Europe/Istanbul", "Atlantic/Canary",
	"America/St_Johns", "America/Halifax", "America/New_York", "America/Toronto", "America/Chicago",
	"America/Denver", "America/Phoenix", "America/Los_Angeles", "America/Vancouver", "America/Anchorage",
	"Pacific/Honolulu", "America/Mexico_City", "America/Sao_Paulo", "Africa/Johannesburg", "Africa/Lagos",
	"Africa/Nairobi", "Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Hong_Kong",
	"Asia/Shanghai", "Asia/Tokyo", "Australia/Perth", "Australia/Adelaide", "Australia/Brisbane",
	"Australia/Sydney", "Australia/Melbourne", "Pacific/Auckland", "UTC",
}

func timezoneOptions(current string) []string {
	if current == "" || slices.Contains(commonTimezones, current) {
		return commonTimezones
	}
	return append([]string{current}, commonTimezones...)
}

// Devices.

type devicesView struct {
	Displays      []dbq.Device
	Sessions      []sessionRow
	ThisDisplayID int64
	OnDisplay     bool
	Me            int64
	DefaultName   string
}

type sessionRow struct {
	dbq.ListPersonalSessionsRow
	ID      string // hex token hash, for the revoke form
	Current bool
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	s.renderDevices(w, r, http.StatusOK, "", nil)
}

func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, status int, flash string, errs map[string]string) {
	ctx := r.Context()
	id := identityFrom(ctx)
	displays, err := s.Store.Q.ListDevices(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows, err := s.Store.Q.ListPersonalSessions(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := devicesView{Displays: displays, Me: id.Session.Person.ID, DefaultName: "Kitchen"}
	if id.Device != nil {
		data.OnDisplay = true
		data.ThisDisplayID = id.Device.ID
	}
	for _, row := range rows {
		data.Sessions = append(data.Sessions, sessionRow{
			ListPersonalSessionsRow: row,
			ID:                      hex.EncodeToString(row.TokenHash),
			Current:                 string(row.TokenHash) == string(id.Session.TokenHash),
		})
	}
	v := s.view(r, "Devices", data)
	v.Nav = "devices"
	v.Flash = flash
	v.Errors = errs
	s.render(w, r, status, "admin_devices", v)
}

// handleDeviceRegister turns the current device into a household display.
// Its personal sign-in ends: on a display, admin is unlocked by PIN.
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := identityFrom(ctx)
	if id.Device != nil {
		redirect(w, r, "/admin/devices")
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := newForm(r).str("name")
	if name == "" || len([]rune(name)) > 50 {
		s.renderDevices(w, r, http.StatusUnprocessableEntity, "", map[string]string{"name": "Name this display, e.g. Kitchen."})
		return
	}
	token, _, err := s.Auth.RegisterDisplay(ctx, s.HouseholdID, name, id.Session.Person.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Auth.EndSession(ctx, c.Value)
	}
	s.clearCookie(w, r, sessionCookie)
	s.setCookie(w, r, deviceCookie, token, deviceCookieAge)
	redirect(w, r, "/")
}

func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	devID, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	if err := s.Auth.RevokeDevice(ctx, s.HouseholdID, devID, actor(r).Person.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Revoking the display you're standing at drops you to sign-in.
	if dev := identityFrom(ctx).Device; dev != nil && dev.ID == devID {
		s.clearCookie(w, r, deviceCookie)
		s.clearCookie(w, r, sessionCookie)
		redirect(w, r, "/signin")
		return
	}
	redirect(w, r, "/admin/devices")
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	hash, err := hex.DecodeString(r.FormValue("session"))
	if err != nil || len(hash) == 0 {
		http.Error(w, "bad session", http.StatusBadRequest)
		return
	}
	if err := s.Auth.RevokeSession(r.Context(), s.HouseholdID, hash, actor(r).Person.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if string(hash) == string(actor(r).TokenHash) {
		s.clearCookie(w, r, sessionCookie)
		redirect(w, r, "/signin")
		return
	}
	redirect(w, r, "/admin/devices")
}

func (s *Server) handleSignOutOthers(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if err := s.Auth.SignOutEverywhere(r.Context(), s.HouseholdID, a.Person.ID, a.TokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderDevices(w, r, http.StatusOK, "Signed out everywhere else.", nil)
}

// keepToken is the caller's own session, kept when they change their own
// credentials.
func keepToken(r *http.Request, personID int64) []byte {
	if a := actor(r); a != nil && a.Person.ID == personID {
		return a.TokenHash
	}
	return nil
}
