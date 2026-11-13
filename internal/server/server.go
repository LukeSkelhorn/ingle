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
	"sync"
	"sync/atomic"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/lock"
	"github.com/lukeskelhorn/ingle/internal/module"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/takeout"
	"github.com/lukeskelhorn/ingle/web"
)

// Deps are the services the server is built from.
type Deps struct {
	Store *store.Store
	// HouseholdID is the one household of a self-hosted server. It's used
	// when ResolveHousehold is nil, and for the module loops when
	// Households is nil.
	HouseholdID int64
	// ResolveHousehold names the household a request is for, before its
	// display or session is known (sign-in, setup). ok is false when it
	// can't tell; a display or session cookie then decides. Nil means
	// HouseholdID for every request.
	ResolveHousehold func(r *http.Request) (id int64, ok bool, err error)
	// Households lists the households the module loops run for. Nil means
	// just HouseholdID.
	Households func(ctx context.Context) ([]int64, error)
	// Routes adds a program's own routes (e.g. Hosted's sign-up and
	// billing) behind the same cross-origin check and identity as Ingle's.
	// Wrap them with RequireAdmin or RequireViewer as needed.
	Routes func(s *Server, mux *http.ServeMux)
	// AdminLinks are extra links in the Admin bar, after the modules'.
	AdminLinks []module.NavItem
	Auth       *auth.Service
	Settings   *settings.Service
	Chores     *chores.Service
	Household  *household.Service
	Avatars    *avatar.Store
	Photos     *photos.Library
	Calendars  *calendar.Service
	Lock       *lock.Service
	// Modules is which features each household has on. New fills in an
	// empty one if it is nil, and registers Ingle's modules in it.
	Modules *module.Service
	// Takeout exports and deletes the household (Admin › Settings). Nil
	// hides both.
	Takeout *takeout.Service
	Locale  locale.Locale
	Log     *slog.Logger
	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// Version is the build's version ("dev" when not built for a release).
	Version string
	// SignIn hands adults' sign-in to another service (Hosted's). Nil
	// keeps Ingle's own email and password sign-in and first-run setup.
	SignIn *SignIn
}

// SignIn is another service that signs adults in.
type SignIn struct {
	// URL is where /signin sends people, e.g. "/auth/signin".
	URL string
	// OnSignOut runs after Ingle ends a personal session, e.g. to end the
	// other service's session too. A non-empty URL is where the browser
	// goes next, instead of URL. Locking admin on a display doesn't call
	// it.
	OnSignOut func(r *http.Request) string
}

// Server serves the Ingle web UI.
type Server struct {
	Deps
	db     *sql.DB
	pages  *pagePool
	now    func() time.Time
	events *hub
	// lockEvents tells every open page the displays locked or unlocked.
	lockEvents *hub

	// loopKicks wake the module loops when the modules change.
	loopKicks    []chan struct{}
	loopsStarted atomic.Bool

	// setupDone holds the households known to be set up (see markSetUp).
	setupDone sync.Map
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
	if s.Modules == nil {
		s.Modules = module.NewService(d.Store, module.NewRegistry())
	}
	s.registerModules(s.Modules.Registry())
	// Switching a module on or off changes every page; displays reload.
	s.Modules.OnChange(func(householdID int64) {
		s.lockEvents.publish(householdID, "")
		s.kickLoops()
	})
	p, err := loadPages(s.funcs())
	if err != nil {
		return nil, err
	}
	s.pages = newPagePool(p)
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
	mux.Handle("GET /screensaver", viewer(s.handleScreensaver))
	mux.Handle("POST /lock", viewer(s.handleLock))
	mux.Handle("POST /unlock", viewer(s.handleUnlock))
	mux.Handle("POST /activity", viewer(s.handleActivity))
	mux.Handle("GET /board", viewer(s.handleBoard))
	mux.Handle("GET /board/events", viewer(s.handleBoardEvents))
	mux.Handle("GET /board/people/{id}", viewer(s.handlePersonSheet))
	mux.Handle("GET /board/calendar", viewer(s.handleCalendarView))
	mux.Handle("GET /board/calendar/day/{date}", viewer(s.handleCalendarDay))
	mux.Handle("GET /board/calendar/chores/{id}/{date}", viewer(s.handleCalendarChore))
	mux.Handle("GET /board/pool", viewer(s.handlePoolSheet))
	mux.Handle("GET /board/occurrences/{id}/pick", viewer(s.handlePick))
	mux.Handle("POST /board/occurrences/{id}/claim", viewer(s.handleClaim))
	mux.Handle("POST /board/occurrences/{id}/complete", viewer(s.handleComplete))
	mux.Handle("POST /board/occurrences/{id}/release", viewer(s.handleRelease))
	mux.Handle("POST /board/completions/{id}/undo", viewer(s.handleUndo))
	mux.Handle("GET /board/completions/{id}/review", viewer(s.handleReviewSheet))
	mux.Handle("POST /board/completions/{id}/approve", viewer(s.boardReview(s.Chores.Approve, "Approved. Points added.")))
	mux.Handle("POST /board/completions/{id}/reject", viewer(s.boardReview(s.Chores.Reject, "Not counted. It's back on the board.")))
	mux.Handle("POST /board/completions/{id}/reverse", viewer(s.boardReview(s.Chores.Reverse, "Reversed. Points taken back.")))

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
	mux.Handle("POST /admin/people/{id}/points", admin(s.handleAdjustPoints))
	mux.Handle("POST /admin/people/{id}/points/{entry}/undo", admin(s.handleUndoAdjustment))

	mux.Handle("GET /admin/chores", admin(s.handleChores))
	mux.Handle("GET /admin/chores/new", admin(s.handleChoreNew))
	mux.Handle("POST /admin/chores", admin(s.handleChoreCreate))
	mux.Handle("GET /admin/chores/{id}", admin(s.handleChoreEdit))
	mux.Handle("POST /admin/chores/{id}", admin(s.handleChoreUpdate))
	mux.Handle("POST /admin/chores/{id}/archive", admin(s.handleChoreArchive))

	mux.Handle("GET /admin/completions", admin(s.handleCompletions))
	mux.Handle("POST /admin/completions/{id}/approve", admin(s.adminReview(s.Chores.Approve, "Approved.")))
	mux.Handle("POST /admin/completions/{id}/reject", admin(s.adminReview(s.Chores.Reject, "Not counted; the chore is back on the board.")))
	mux.Handle("POST /admin/completions/missed/{id}/done", admin(s.handleMarkDone))
	mux.Handle("POST /admin/completions/missed/{id}/dismiss", admin(s.handleDismissMissed))
	mux.Handle("POST /admin/completions/missed/dismiss", admin(s.handleDismissMissed))
	mux.Handle("POST /admin/completions/missed/restore", admin(s.handleRestoreMissed))
	mux.Handle("POST /admin/completions/{id}/reverse", admin(s.adminReview(s.Chores.Reverse, "Reversed; points taken back and the chore is back on the board.")))

	mux.Handle("GET /admin/devices", admin(s.handleDevices))
	mux.Handle("POST /admin/devices/register", admin(s.handleDeviceRegister))
	mux.Handle("POST /admin/devices/{id}/revoke", admin(s.handleDeviceRevoke))
	mux.Handle("POST /admin/sessions/revoke", admin(s.handleSessionRevoke))
	mux.Handle("POST /admin/sessions/others", admin(s.handleSignOutOthers))

	mux.Handle("GET /admin/modules", admin(s.handleModules))
	mux.Handle("POST /admin/modules", admin(s.handleModulesUpdate))

	mux.Handle("GET /admin/settings", admin(s.handleSettings))
	mux.Handle("POST /admin/settings", admin(s.handleSettingsUpdate))
	mux.Handle("POST /admin/settings/unlock-code", admin(s.handleUnlockCodeSet))
	mux.Handle("POST /admin/settings/unlock-code/clear", admin(s.handleUnlockCodeClear))
	if s.Takeout != nil {
		mux.Handle("GET /admin/household/export", admin(s.handleHouseholdExport))
		mux.Handle("POST /admin/household/delete", admin(s.handleHouseholdDelete))
	}

	// Modules mount their own routes, hidden while they're off.
	for _, m := range s.Modules.Registry().All() {
		m.Routes(moduleRouter{s: s, mux: mux, id: m.ID()})
	}

	if s.Routes != nil {
		s.Routes(s, mux)
	}

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
		loc.Location = s.Settings.Location(ctx, householdIn(ctx))
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

// withPages runs fn with templates bound to r's household.
func (s *Server) withPages(r *http.Request, fn func(*pages) error) error {
	p, err := s.pages.get()
	if err != nil {
		return err
	}
	defer s.pages.put(p)
	p.bind(s.householdFuncs(r.Context()))
	return fn(p)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	var buf bytes.Buffer
	if err := s.withPages(r, func(p *pages) error { return p.execute(&buf, page, data) }); err != nil {
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
	if err := s.withPages(r, func(p *pages) error { return p.partial(&buf, name, data) }); err != nil {
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
			s.events.publish(hhOf(r), "")
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
