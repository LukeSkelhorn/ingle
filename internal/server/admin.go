package server

import (
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type adminHome struct {
	People  int
	Chores  int
	Rewards int
	Pending int
	Bank    int // bank requests waiting
	// StaleBanks are children whose bank balance wants checking.
	StaleBanks []chores.StaleBank
	// ModulesChosen is false until Admin › Modules has been saved once.
	ModulesChosen bool
	// ModuleNames lists every module, e.g. "Rewards, My money, Photos and
	// Calendar", for the card shown until they're chosen.
	ModuleNames string
	Lock        lockView
	Activity    []dbq.ListRecentActivityWithActorRow
}

func (s *Server) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	people, err := s.Household.People(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	chs, err := s.Chores.Chores(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rewards, err := s.Household.Rewards(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pending, err := s.Store.Q.ListPendingCompletions(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	claims, err := s.Store.Q.ListPendingRedemptions(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	act, err := s.Store.Q.ListRecentActivityWithActor(ctx, dbq.ListRecentActivityWithActorParams{HouseholdID: hhOf(r), Limit: 20})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lv, err := s.boardLock(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !s.moduleOn(ctx, modRewards) {
		claims = nil // they wait, unseen, until Rewards is back on
	}
	mods, err := s.Modules.State(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var names string
	if !mods.Chosen {
		var ids []string
		for _, m := range s.Modules.Registry().All() {
			ids = append(ids, m.ID())
		}
		names = s.moduleNames(ids)
	}
	var bank int
	var stale []chores.StaleBank
	if s.moduleOn(ctx, modMoney) {
		if bank, err = s.Chores.PendingTransferCount(ctx, hhOf(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
		if stale, err = s.Chores.StaleBanks(ctx, hhOf(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	v := s.view(r, "Admin", adminHome{
		People: len(people), Chores: len(chs), Rewards: len(rewards), Pending: len(pending) + len(claims), Bank: bank, StaleBanks: stale,
		ModulesChosen: mods.Chosen, ModuleNames: names, Lock: lv, Activity: act,
	})
	v.Nav = "home"
	s.render(w, r, http.StatusOK, "admin_home", v)
}

// activityActor names who did something when no person did (the schedule
// or idle lock, the household code).
func activityActor(verb string) string {
	if strings.HasPrefix(verb, "board.") && verb != "board.unlocked_code" {
		return "Ingle"
	}
	return "Someone"
}

// activityLine is what the feed says after who did it: "asked to move
// £5.00 to their bank", "approved moving £5.00 to Thomas's bank", or
// "claimed Lego set".
func (s *Server) activityLine(a dbq.ListRecentActivityWithActorRow) string {
	if a.SubjectPence == nil && a.SubjectPerson != nil {
		switch a.Verb {
		case "bank.updated":
			return "updated " + *a.SubjectPerson + "'s bank balance"
		case "bank.removed":
			return "removed " + *a.SubjectPerson + "'s bank balance"
		}
	}
	if a.SubjectPence != nil && a.SubjectPerson != nil {
		amount, whose := s.Locale.FormatMoney(*a.SubjectPence), *a.SubjectPerson+"'s"
		switch a.Verb {
		case "transfer.requested":
			return "asked to move " + amount + " to their bank"
		case "transfer.cancelled":
			return "cancelled moving " + amount + " to their bank"
		case "transfer.approved":
			return "approved moving " + amount + " to " + whose + " bank"
		case "transfer.declined":
			return "declined moving " + amount + " to " + whose + " bank"
		case "transfer.paid":
			return "moved " + amount + " to " + whose + " bank"
		}
	}
	d := describeActivity(a.Verb)
	if a.SubjectTitle != nil {
		d += " " + *a.SubjectTitle
	}
	return d
}

// describeActivity turns an activity verb into a short phrase for the feed.
func describeActivity(verb string) string {
	if d, ok := activityText[verb]; ok {
		return d
	}
	return strings.ReplaceAll(verb, ".", " ")
}

var activityText = map[string]string{
	"household.setup":         "set up the household",
	"settings.updated":        "changed settings",
	"modules.updated":         "changed the modules",
	"admin.unlocked":          "unlocked admin on a display",
	"admin.signed_in":         "signed in",
	"device.registered":       "registered a household display",
	"device.revoked":          "removed a household display",
	"session.revoked":         "signed out a device",
	"person.created":          "added a family member",
	"person.updated":          "edited a family member",
	"person.archived":         "archived a family member",
	"person.restored":         "restored a family member",
	"person.pin_set":          "set a PIN",
	"person.pin_cleared":      "removed a PIN",
	"person.login_set":        "set a sign-in",
	"person.login_cleared":    "removed a sign-in",
	"chore.created":           "added a chore",
	"chore.updated":           "edited a chore",
	"chore.archived":          "archived a chore",
	"chore.claimed":           "claimed a chore",
	"chore.released":          "gave back a chore",
	"chore.reversed":          "reversed a chore",
	"chore.completed":         "completed a chore",
	"chore.undone":            "undid a chore",
	"chore.approved":          "approved a chore",
	"chore.rejected":          "sent a chore back",
	"reward.created":          "added a reward",
	"reward.updated":          "edited a reward",
	"reward.archived":         "archived a reward",
	"reward.restored":         "restored a reward",
	"reward.requested":        "asked for",
	"reward.redeemed":         "claimed",
	"reward.declined":         "declined a claim for",
	"reward.cancelled":        "cancelled a claim for",
	"points.adjusted":         "changed someone's points",
	"points.undone":           "undid a points change",
	"transfer.requested":      "asked to move money to their bank",
	"transfer.cancelled":      "cancelled moving money to their bank",
	"transfer.approved":       "approved moving money to a bank",
	"transfer.declined":       "declined moving money to a bank",
	"transfer.paid":           "moved money to a bank",
	"bank.updated":            "updated a bank balance",
	"bank.removed":            "removed a bank balance",
	"photo.added":             "added a photo",
	"photo.removed":           "removed a photo",
	"calendar.added":          "added a calendar",
	"calendar.removed":        "removed a calendar",
	"lock.code_set":           "set the household unlock code",
	"lock.code_cleared":       "removed the household unlock code",
	"board.locked":            "locked the displays",
	"board.locked_schedule":   "locked the displays on schedule",
	"board.locked_idle":       "locked the displays after a long idle",
	"board.unlocked":          "unlocked the displays",
	"board.unlocked_code":     "unlocked the displays with the household code",
	"board.unlocked_schedule": "unlocked the displays on schedule",
	"holiday.added":           "added a holiday",
	"holiday.ended":           "ended a holiday early",
	"holiday.removed":         "removed a holiday",
}

// Settings.

type settingsForm struct {
	// CanTakeout shows "Your household's data"; StartsAgain says deleting
	// leads back to first-run setup (self-hosted).
	CanTakeout, StartsAgain bool

	HouseholdName          string
	Timezone               string
	DayStartsAt            string
	AdminIdleMinutes       int64
	ScreensaverIdleMinutes int64
	CalendarIdleMinutes    int64
	LockDays               int64
	LockFrom               string
	LockUntil              string
	LockIdleHours          int64
	HasUnlockCode          bool
	Timezones              []string
	Holiday                holidayForm
}

// weekdayOption is one checkbox of the lock schedule.
type weekdayOption struct {
	Bit     int
	Name    string
	Checked bool
}

// LockWeekdays lists Monday to Sunday for the schedule checkboxes.
func (f settingsForm) LockWeekdays() []weekdayOption {
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	out := make([]weekdayOption, 0, 7)
	for _, d := range []int{1, 2, 3, 4, 5, 6, 0} {
		out = append(out, weekdayOption{Bit: d, Name: names[d], Checked: f.LockDays&(1<<d) != 0})
	}
	return out
}

func (s *Server) settingsForm(set settings.Settings) settingsForm {
	return settingsForm{
		HouseholdName:          set.HouseholdName,
		Timezone:               set.Location.String(),
		DayStartsAt:            set.DayStartsAt,
		AdminIdleMinutes:       set.AdminIdleMinutes,
		ScreensaverIdleMinutes: set.ScreensaverIdleMinutes,
		CalendarIdleMinutes:    set.CalendarIdleMinutes,
		LockDays:               set.LockDays,
		LockFrom:               set.LockFrom,
		LockUntil:              set.LockUntil,
		LockIdleHours:          set.LockIdleHours,
		HasUnlockCode:          set.HasUnlockCode(),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderSettings(w, r, http.StatusOK, s.settingsForm(set), nil, "")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, f settingsForm, errs map[string]string, flash string) {
	f.Timezones = timezoneOptions(f.Timezone)
	f.CanTakeout, f.StartsAgain = s.Takeout != nil, s.ResolveHousehold == nil
	if err := s.loadHolidays(r, &f.Holiday); err != nil {
		s.serverError(w, r, err)
		return
	}
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
		HouseholdName:          f.str("household_name"),
		Timezone:               f.str("timezone"),
		DayStartsAt:            f.str("day_starts_at"),
		AdminIdleMinutes:       f.int("admin_idle_minutes", "Choose between 1 and 60 minutes."),
		ScreensaverIdleMinutes: f.int("screensaver_idle_minutes", "Choose between 0 (never) and 120 minutes."),
		CalendarIdleMinutes:    f.int("calendar_idle_minutes", "Choose between 0 (never) and 60 minutes."),
		LockDays:               weekdayMask(r.PostForm["lock_day"]),
		LockFrom:               f.str("lock_from"),
		LockUntil:              f.str("lock_until"),
		LockIdleHours:          f.int("lock_idle_hours", "Choose between 0 (never) and 72 hours."),
	}
	if set, err := s.Settings.Get(r.Context(), hhOf(r)); err == nil {
		data.HasUnlockCode = set.HasUnlockCode()
	}
	if !f.ok() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, data, f.errs, "")
		return
	}
	err := s.Settings.Update(r.Context(), hhOf(r), actor(r).Person.ID, settings.Input{
		HouseholdName: data.HouseholdName, Timezone: data.Timezone,
		DayStartsAt: data.DayStartsAt, AdminIdleMinutes: data.AdminIdleMinutes,
		ScreensaverIdleMinutes: data.ScreensaverIdleMinutes,
		CalendarIdleMinutes:    data.CalendarIdleMinutes,
		LockDays:               data.LockDays, LockFrom: data.LockFrom, LockUntil: data.LockUntil, LockIdleHours: data.LockIdleHours,
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

// weekdayMask turns the schedule's checked weekdays ("0" = Sunday … "6")
// into a bitmask, ignoring anything else.
func weekdayMask(days []string) int64 {
	var mask int64
	for _, d := range days {
		if n, err := strconv.Atoi(d); err == nil && n >= 0 && n <= 6 {
			mask |= 1 << n
		}
	}
	return mask
}

// handleUnlockCodeSet sets the household unlock code.
func (s *Server) handleUnlockCodeSet(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if f.str("code") != f.str("code_confirm") {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, s.settingsForm(set), map[string]string{"code_confirm": "The codes don't match."}, "")
		return
	}
	err = s.Auth.SetUnlockCode(r.Context(), hhOf(r), actor(r).Person.ID, f.str("code"))
	if errors.Is(err, auth.ErrInvalidPIN) {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, s.settingsForm(set), map[string]string{"code": "Use 4 to 6 digits."}, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.settingsSaved(w, r, "Unlock code saved.")
}

// handleUnlockCodeClear removes the household unlock code.
func (s *Server) handleUnlockCodeClear(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.ClearUnlockCode(r.Context(), hhOf(r), actor(r).Person.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.settingsSaved(w, r, "Unlock code removed. Adult PINs still unlock the displays.")
}

func (s *Server) settingsSaved(w http.ResponseWriter, r *http.Request, flash string) {
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderSettings(w, r, http.StatusOK, s.settingsForm(set), nil, flash)
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
	displays, err := s.Store.Q.ListDevices(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows, err := s.Store.Q.ListPersonalSessions(ctx, hhOf(r))
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
	token, _, err := s.Auth.RegisterDisplay(ctx, hhOf(r), name, id.Session.Person.ID)
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
	if err := s.Auth.RevokeDevice(ctx, hhOf(r), devID, actor(r).Person.ID); err != nil {
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
	if err := s.Auth.RevokeSession(r.Context(), hhOf(r), hash, actor(r).Person.ID); err != nil {
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
	if err := s.Auth.SignOutEverywhere(r.Context(), hhOf(r), a.Person.ID, a.TokenHash); err != nil {
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
