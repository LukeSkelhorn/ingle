// Package server wires HTTP routes for the Ingle board and admin.
package server

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/lock"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/web"
)

// Deps are the services the server is built from.
type Deps struct {
	Store       *store.Store
	HouseholdID int64
	Auth        *auth.Service
	Settings    *settings.Service
	Chores      *chores.Service
	Household   *household.Service
	Avatars     *avatar.Store
	Photos      *photos.Library
	Calendars   *calendar.Service
	Lock        *lock.Service
	Locale      locale.Locale
	Log         *slog.Logger
	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// Version is the build's version ("dev" when not built for a release).
	Version string
}

// Server serves the Ingle web UI.
type Server struct {
	Deps
	db     *sql.DB
	pages  *pages
	now    func() time.Time
	events *hub
	// lockEvents tells every open page the displays locked or unlocked.
	lockEvents *hub

	// setupDone latches once an adult can sign in; it never goes back
	// because the last such adult can't be archived or stripped.
	setupDone atomic.Bool
}

// New builds a Server.
func New(d Deps) (*Server, error) {
	s := &Server{Deps: d, now: time.Now, events: newHub(), lockEvents: newHub()}
	if d.Lock != nil {
		// Every open page hears about a lock or unlock; displays reload.
		d.Lock.OnChange(func(householdID int64) { s.lockEvents.publish(householdID, "") })
	}
	if d.Store != nil {
		s.db = d.Store.DB
	}
	p, err := loadPages(s.funcs())
	if err != nil {
		return nil, err
	}
	s.pages = p
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Server) SetClock(now func() time.Time) { s.now = now }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err) // "static" is embedded at compile time
	}
	files := http.FileServerFS(static)

	mux := http.NewServeMux()

	// Open routes: no household data.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticCache(files)))
	// The manifest and service worker must live at the root so the SW can
	// control the whole origin.
	mux.HandleFunc("GET /manifest.webmanifest", serveFile(static, "manifest.webmanifest", "application/manifest+json"))
	mux.HandleFunc("GET /sw.js", serveServiceWorker(static))
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /signin", s.handleSignInForm)
	mux.HandleFunc("POST /signin", s.handleSignIn)
	mux.HandleFunc("POST /signout", s.handleSignOut)
	mux.HandleFunc("GET /admin/unlock", s.notWhileLocked(s.handleUnlockForm))
	mux.HandleFunc("POST /admin/unlock", s.notWhileLocked(s.handleAdminUnlock))
	mux.HandleFunc("POST /admin/lock", s.handleSignOut)

	// Viewer routes: a household display or a signed-in adult.
	viewer := func(h http.HandlerFunc) http.Handler { return s.requireViewer(s.lockGate(h)) }
	mux.Handle("GET /{$}", viewer(s.handleIndex))
	mux.Handle("GET /clock", viewer(s.handleClock))
	mux.Handle("GET /avatars/{name}", viewer(s.handleAvatar))
	mux.Handle("GET /photos/{kind}/{name}", viewer(s.handlePhoto))
	mux.Handle("GET /screensaver", viewer(s.handleScreensaver))
	mux.Handle("GET /screensaver/photos", viewer(s.handleSlides))
	mux.Handle("POST /lock", viewer(s.handleLock))
	mux.Handle("POST /unlock", viewer(s.handleUnlock))
	mux.Handle("POST /activity", viewer(s.handleActivity))
	mux.Handle("GET /board", viewer(s.handleBoard))
	mux.Handle("GET /board/events", viewer(s.handleBoardEvents))
	mux.Handle("GET /board/people/{id}", viewer(s.handlePersonSheet))
	mux.Handle("GET /board/calendar", viewer(s.handleCalendarView))
	mux.Handle("GET /board/calendar/day/{date}", viewer(s.handleCalendarDay))
	mux.Handle("GET /board/calendar/chores/{id}/{date}", viewer(s.handleCalendarChore))
	mux.Handle("GET /board/occurrences/{id}/pick", viewer(s.handlePick))
	mux.Handle("POST /board/occurrences/{id}/claim", viewer(s.handleClaim))
	mux.Handle("POST /board/occurrences/{id}/complete", viewer(s.handleComplete))
	mux.Handle("POST /board/occurrences/{id}/release", viewer(s.handleRelease))
	mux.Handle("POST /board/completions/{id}/undo", viewer(s.handleUndo))
	mux.Handle("GET /board/completions/{id}/review", viewer(s.handleReviewSheet))
	mux.Handle("POST /board/completions/{id}/approve", viewer(s.boardReview(s.Chores.Approve, "Approved. Points added.")))
	mux.Handle("POST /board/completions/{id}/reject", viewer(s.boardReview(s.Chores.Reject, "Not counted. It's back on the board.")))
	mux.Handle("POST /board/completions/{id}/reverse", viewer(s.boardReview(s.Chores.Reverse, "Reversed. Points taken back.")))
	mux.Handle("POST /board/rewards/{id}/claim", viewer(s.handleRewardClaim))
	mux.Handle("POST /board/people/{id}/goal", viewer(s.handleGoalSet))
	mux.Handle("POST /board/people/{id}/goal/clear", viewer(s.handleGoalClear))
	mux.Handle("GET /board/redemptions/{id}/review", viewer(s.handleClaimReviewSheet))
	mux.Handle("POST /board/redemptions/{id}/approve", viewer(s.boardReview(s.Chores.ApproveRedemption, "Approved. Enjoy! 🎁")))
	mux.Handle("POST /board/redemptions/{id}/decline", viewer(s.boardReview(s.Chores.DeclineRedemption, "Declined. Points are free again.")))
	mux.Handle("POST /board/redemptions/{id}/cancel", viewer(s.handleClaimCancel))

	// Admin routes: a signed-in adult (PIN on a display, password elsewhere).
	// Any admin write may change what the board shows, so open boards are
	// told to refresh afterwards.
	admin := func(h http.HandlerFunc) http.Handler { return s.requireAdmin(s.publishAfterWrite(h)) }
	mux.Handle("GET /admin", admin(s.handleAdminHome))

	mux.Handle("GET /admin/people", admin(s.handlePeople))
	mux.Handle("GET /admin/people/new", admin(s.handlePersonNew))
	mux.Handle("POST /admin/people", admin(s.handlePersonCreate))
	mux.Handle("GET /admin/people/{id}", admin(s.handlePersonEdit))
	mux.Handle("POST /admin/people/{id}", admin(s.handlePersonUpdate))
	mux.Handle("POST /admin/people/{id}/archive", admin(s.handlePersonArchive))
	mux.Handle("POST /admin/people/{id}/restore", admin(s.handlePersonRestore))
	mux.Handle("POST /admin/people/{id}/pin", admin(s.handlePersonSetPIN))
	mux.Handle("POST /admin/people/{id}/pin/clear", admin(s.handlePersonClearPIN))
	mux.Handle("POST /admin/people/{id}/login", admin(s.handlePersonSetLogin))
	mux.Handle("POST /admin/people/{id}/login/clear", admin(s.handlePersonClearLogin))

	mux.Handle("GET /admin/chores", admin(s.handleChores))
	mux.Handle("GET /admin/chores/new", admin(s.handleChoreNew))
	mux.Handle("POST /admin/chores", admin(s.handleChoreCreate))
	mux.Handle("GET /admin/chores/{id}", admin(s.handleChoreEdit))
	mux.Handle("POST /admin/chores/{id}", admin(s.handleChoreUpdate))
	mux.Handle("POST /admin/chores/{id}/archive", admin(s.handleChoreArchive))

	mux.Handle("GET /admin/rewards", admin(s.handleRewards))
	mux.Handle("GET /admin/rewards/new", admin(s.handleRewardNew))
	mux.Handle("POST /admin/rewards", admin(s.handleRewardCreate))
	mux.Handle("GET /admin/rewards/{id}", admin(s.handleRewardEdit))
	mux.Handle("POST /admin/rewards/{id}", admin(s.handleRewardUpdate))
	mux.Handle("POST /admin/rewards/{id}/archive", admin(s.handleRewardArchive))
	mux.Handle("POST /admin/rewards/{id}/restore", admin(s.handleRewardRestore))

	mux.Handle("GET /admin/completions", admin(s.handleCompletions))
	mux.Handle("POST /admin/completions/{id}/approve", admin(s.adminReview(s.Chores.Approve, "Approved.")))
	mux.Handle("POST /admin/completions/{id}/reject", admin(s.adminReview(s.Chores.Reject, "Not counted; the chore is back on the board.")))
	mux.Handle("POST /admin/completions/missed/{id}/done", admin(s.handleMarkDone))
	mux.Handle("POST /admin/completions/missed/{id}/dismiss", admin(s.handleDismissMissed))
	mux.Handle("POST /admin/completions/missed/dismiss", admin(s.handleDismissMissed))
	mux.Handle("POST /admin/completions/missed/restore", admin(s.handleRestoreMissed))
	mux.Handle("POST /admin/completions/{id}/reverse", admin(s.adminReview(s.Chores.Reverse, "Reversed; points taken back and the chore is back on the board.")))

	mux.Handle("POST /admin/rewards/claims/{id}/approve", admin(s.adminReview(s.Chores.ApproveRedemption, "Reward approved.")))
	mux.Handle("POST /admin/rewards/claims/{id}/decline", admin(s.adminReview(s.Chores.DeclineRedemption, "Reward declined; the points are free again.")))

	mux.Handle("GET /admin/photos", admin(s.handlePhotos))
	mux.Handle("POST /admin/photos", admin(s.handlePhotoUpload))
	mux.Handle("POST /admin/photos/{id}/delete", admin(s.handlePhotoDelete))

	mux.Handle("GET /admin/calendars", admin(s.handleCalendars))
	mux.Handle("POST /admin/calendars", admin(s.handleCalendarAdd))
	mux.Handle("POST /admin/calendars/refresh", admin(s.handleCalendarRefresh))
	mux.Handle("POST /admin/calendars/{id}/delete", admin(s.handleCalendarDelete))

	mux.Handle("GET /admin/devices", admin(s.handleDevices))
	mux.Handle("POST /admin/devices/register", admin(s.handleDeviceRegister))
	mux.Handle("POST /admin/devices/{id}/revoke", admin(s.handleDeviceRevoke))
	mux.Handle("POST /admin/sessions/revoke", admin(s.handleSessionRevoke))
	mux.Handle("POST /admin/sessions/others", admin(s.handleSignOutOthers))

	mux.Handle("GET /admin/settings", admin(s.handleSettings))
	mux.Handle("POST /admin/settings", admin(s.handleSettingsUpdate))
	mux.Handle("POST /admin/settings/unlock-code", admin(s.handleUnlockCodeSet))
	mux.Handle("POST /admin/settings/unlock-code/clear", admin(s.handleUnlockCodeClear))

	// Outermost first: reject cross-origin writes, identify the caller, then
	// send everything to /setup until the household is set up.
	csrf := http.NewCrossOriginProtection()
	return csrf.Handler(s.identify(s.requireSetup(mux)))
}

type clockView struct {
	ISO, Time, Weekday, Date string
}

func (s *Server) clock(ctx context.Context) clockView {
	loc := s.Locale
	if s.Settings != nil {
		loc.Location = s.Settings.Location(ctx, s.HouseholdID)
	}
	now := loc.In(s.now())
	return clockView{
		ISO:     now.Format(time.RFC3339),
		Time:    loc.FormatTime(now),
		Weekday: now.Weekday().String(),
		Date:    loc.FormatDate(now),
	}
}

func (s *Server) handleClock(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("big") == "1" {
		s.renderPartial(w, r, "big_clock", s.clock(r.Context()))
		return
	}
	s.renderPartial(w, r, "clock", s.clock(r.Context()))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			s.Log.ErrorContext(r.Context(), "health check failed", slog.Any("err", err))
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok " + s.version() + "\n"))
}

func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	f, err := s.Avatars.Open(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Names are random and never reused, so the content never changes.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	var buf bytes.Buffer
	if err := s.pages.execute(&buf, page, data); err != nil {
		s.Log.ErrorContext(r.Context(), "render template", slog.String("page", page), slog.Any("err", err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderPartial(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.renderPartialStatus(w, r, http.StatusOK, name, data)
}

func (s *Server) renderPartialStatus(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages.partial(&buf, name, data); err != nil {
		s.Log.ErrorContext(r.Context(), "render partial", slog.String("partial", name), slog.Any("err", err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// publishAfterWrite tells open boards to refresh after a non-GET request.
func (s *Server) publishAfterWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.events.publish(s.HouseholdID, "")
		}
	}
}

// serverError logs err and shows a generic error page.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.ErrorContext(r.Context(), "request failed", slog.String("path", r.URL.Path), slog.Any("err", err))
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

func serveFile(fsys fs.FS, name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

// staticCache lets browsers keep versioned asset URLs (?v=, from the asset
// template function) for good, and makes them check anything else.
func staticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// serveServiceWorker serves sw.js with its cache named after the static
// files' hash, so a build with new CSS or JS replaces the cached copies.
func serveServiceWorker(fsys fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(fsys, "sw.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(web.StaticVersion())))
	}
}

func (s *Server) version() string {
	if s.Version == "" {
		return "dev"
	}
	return s.Version
}
