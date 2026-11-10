package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Cookie names. They keep the app's old name, Domus, so devices and
// sessions from before the rename stay signed in.
const (
	deviceCookie  = "domus_device"
	sessionCookie = "domus_session"
)

// deviceCookieAge keeps a household display registered for ten years (or
// until revoked under Admin › Devices).
const deviceCookieAge = 10 * 365 * 24 * time.Hour

type (
	ctxKey       struct{}
	householdKey struct{}
)

// hhOf returns the household r is for, or 0 when none is known: an open
// page on a server with many households and nobody signed in.
func hhOf(r *http.Request) int64 { return householdIn(r.Context()) }

// householdIn returns the household stored in ctx by identify, or 0.
func householdIn(ctx context.Context) int64 {
	id, _ := ctx.Value(householdKey{}).(int64)
	return id
}

// withHousehold returns ctx for the household id.
func withHousehold(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, householdKey{}, id)
}

// resolveHousehold asks the ResolveHousehold hook which household r is
// for. Without the hook it's the server's one household.
func (s *Server) resolveHousehold(r *http.Request) (int64, error) {
	if s.ResolveHousehold == nil {
		return s.HouseholdID, nil
	}
	id, ok, err := s.ResolveHousehold(r)
	if err != nil || !ok {
		return 0, err
	}
	return id, nil
}

// identity is who is making the request, resolved once per request.
type identity struct {
	Device  *dbq.Device
	Session *auth.Session
}

func identityFrom(ctx context.Context) identity {
	id, _ := ctx.Value(ctxKey{}).(identity)
	return id
}

// identify resolves the household and the display and session cookies.
// The household is the one the resolver names, or else the display's or
// session's own. A cookie for another household is cleared.
func (s *Server) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOpenAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		var id identity
		hh, err := s.resolveHousehold(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}

		if c, err := r.Cookie(deviceCookie); err == nil {
			dev, err := s.Auth.Display(ctx, c.Value)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			if dev == nil || (hh != 0 && dev.HouseholdID != hh) {
				s.clearCookie(w, r, deviceCookie)
			} else {
				id.Device = dev
				hh = dev.HouseholdID
			}
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			sess, err := s.Auth.Session(ctx, c.Value, id.Device)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			// On a household display only PIN (display) sessions count, so
			// the idle auto-lock always applies there.
			if sess != nil && id.Device != nil && sess.Kind != auth.KindDisplay {
				_ = s.Auth.EndSession(ctx, c.Value)
				sess = nil
			}
			if sess == nil || (hh != 0 && sess.Person.HouseholdID != hh) {
				s.clearCookie(w, r, sessionCookie)
			} else {
				id.Session = sess
				hh = sess.Person.HouseholdID
			}
		}
		ctx = withHousehold(context.WithValue(ctx, ctxKey{}, id), hh)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireSetup sends every page to /setup until an adult can sign in. A
// request with no household has nothing to set up.
func (s *Server) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hh := hhOf(r)
		if hh == 0 || s.isSetUp(hh) || isOpenAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		needs, err := s.Household.NeedsSetup(r.Context(), hh)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !needs {
			s.markSetUp(hh)
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/setup" {
			next.ServeHTTP(w, r)
			return
		}
		redirect(w, r, "/setup")
	})
}

// isSetUp reports whether household hh is known to be set up.
func (s *Server) isSetUp(hh int64) bool {
	_, ok := s.setupDone.Load(hh)
	return ok
}

// markSetUp latches household hh as set up; it never goes back because the
// last adult who can sign in can't be archived or stripped.
func (s *Server) markSetUp(hh int64) { s.setupDone.Store(hh, struct{}{}) }

// requireViewer allows household displays and signed-in adults.
func (s *Server) requireViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identityFrom(r.Context())
		if id.Device == nil && id.Session == nil {
			redirect(w, r, "/signin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdmin allows signed-in adults: PIN-unlocked on a display, or signed
// in with a password elsewhere.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identityFrom(r.Context())
		if id.Session != nil {
			next.ServeHTTP(w, r)
			return
		}
		if id.Device != nil {
			redirect(w, r, "/admin/unlock")
			return
		}
		redirect(w, r, "/signin")
	})
}

// isOpenAsset reports paths that never need identity or setup.
func isOpenAsset(p string) bool {
	return p == "/healthz" || p == "/manifest.webmanifest" || p == "/sw.js" || strings.HasPrefix(p, "/static/")
}

// redirect sends the browser to url; htmx requests get HX-Redirect so the
// whole page navigates instead of swapping a fragment.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	// Secure is set whenever the request came over HTTPS (directly or via a
	// proxy); plain HTTP is supported for home LANs without TLS.
	c := &http.Cookie{ //nolint:gosec // G124: Secure is dynamic, see above
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge > 0 {
		c.MaxAge = int(maxAge.Seconds())
	}
	http.SetCookie(w, c)
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is dynamic, as in setCookie
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
}

// view builds the common page data for r.
func (s *Server) view(r *http.Request, title string, data any) view {
	id := identityFrom(r.Context())
	v := view{Title: title, Data: data, Version: s.version()}
	screensaverSeconds, calendarSeconds := 0, 0
	if s.Settings != nil {
		if set, err := s.Settings.Get(r.Context(), hhOf(r)); err == nil {
			v.Household = set.HouseholdName
			v.Viewer.IdleSeconds = int(set.AdminIdle().Seconds())
			screensaverSeconds = int(set.ScreensaverIdle().Seconds())
			calendarSeconds = int(set.CalendarIdle().Seconds())
		}
	}
	if id.Device != nil {
		v.Viewer.IsDisplay = true
		v.Viewer.DisplayName = id.Device.Name
		v.Viewer.ScreensaverSeconds = screensaverSeconds
		v.Viewer.CalendarSeconds = calendarSeconds
	}
	if id.Session != nil {
		p := id.Session.Person
		v.Viewer.Adult = &p
		v.Viewer.Kind = id.Session.Kind
	}
	return v
}

// actor returns the signed-in adult. Only call behind requireAdmin.
func actor(r *http.Request) *auth.Session {
	return identityFrom(r.Context()).Session
}
