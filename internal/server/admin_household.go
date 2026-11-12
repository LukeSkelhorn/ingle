package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9]+`)

// handleHouseholdExport downloads all of the household's data as a zip.
func (s *Server) handleHouseholdExport(w http.ResponseWriter, r *http.Request) {
	hh := hhOf(r)
	set, err := s.Settings.Get(r.Context(), hh)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := strings.Trim(strings.ToLower(unsafeFileChars.ReplaceAllString(set.HouseholdName, "-")), "-")
	if name == "" {
		name = "household"
	}
	file := fmt.Sprintf("ingle-%s-%s.zip", name, time.Now().In(set.Location).Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.Takeout.Export(r.Context(), hh, w); err != nil {
		// Headers have gone, so the download just stops short.
		s.Log.ErrorContext(r.Context(), "export household", slog.Int64("household", hh), slog.Any("err", err))
		return
	}
	s.Log.InfoContext(r.Context(), "household exported", slog.Int64("household", hh))
}

// handleHouseholdDelete deletes the household and everything in it, once
// its name is typed to confirm. Self-hosted Ingle gets an empty household
// in its place and starts again at first-run setup.
func (s *Server) handleHouseholdDelete(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ctx, hh := r.Context(), hhOf(r)
	set, err := s.Settings.Get(ctx, hh)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), strings.TrimSpace(set.HouseholdName)) {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, s.settingsForm(set),
			map[string]string{"confirm": "Type the household's name exactly, to show you mean it."}, "")
		return
	}

	recreate := s.ResolveHousehold == nil
	if err := s.Takeout.Delete(ctx, hh, recreate); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Settings.Invalidate(hh)
	s.Lock.Invalidate(hh)
	if s.Modules != nil {
		s.Modules.Invalidate(hh)
	}
	s.setupDone.Delete(hh)
	if recreate {
		if err := s.Settings.Ensure(ctx, hh, s.Locale.Location); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.events.publish(hh, "") // open boards reload and find it gone
	s.clearCookie(w, r, sessionCookie)
	s.clearCookie(w, r, deviceCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
