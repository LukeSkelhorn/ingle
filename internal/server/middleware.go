package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Cookie names keep the old Domus name so pre-rename sessions stay signed in.
const (
	deviceCookie  = "domus_device"
	sessionCookie = "domus_session"
)

const deviceCookieAge = 10 * 365 * 24 * time.Hour

type (
	ctxKey struct{}
)

// HouseholdOf returns the household r is for, or 0 when none is known.
func HouseholdOf(r *http.Request) int64 { return hhOf(r) }

// RequireAdmin lets only a signed-in adult of the household reach h.
func (s *Server) RequireAdmin(h http.Handler) http.Handler { return s.requireAdmin(h) }

// RequireViewer lets only a household display or signed-in adult reach h.
func (s *Server) RequireViewer(h http.Handler) http.Handler { return s.requireViewer(h) }

func hhOf(r *http.Request) int64 { return householdIn(r.Context()) }

func householdIn(ctx context.Context) int64 { return store.HouseholdFrom(ctx) }

func withHousehold(ctx context.Context, id int64) context.Context {
	return store.WithHousehold(ctx, id)
}

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

type identity struct {
	Device  *dbq.Device
	Session *auth.Session
}

func identityFrom(ctx context.Context) identity {
	id, _ := ctx.Value(ctxKey{}).(identity)
	return id
}

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
			// On a display only PIN sessions count, so the idle auto-lock always applies.
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

func (s *Server) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hh := hhOf(r)
		if hh == 0 || s.SignIn != nil || s.isSetUp(hh) || isOpenAsset(r.URL.Path) {
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

func (s *Server) signInURL() string {
	if s.SignIn != nil {
		return s.SignIn.URL
	}
	return "/signin"
}

func (s *Server) isSetUp(hh int64) bool {
	_, ok := s.setupDone.Load(hh)
	return ok
}

func (s *Server) markSetUp(hh int64) { s.setupDone.Store(hh, struct{}{}) }

func (s *Server) requireViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identityFrom(r.Context())
		if id.Device == nil && id.Session == nil {
			redirect(w, r, s.signInURL())
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
		redirect(w, r, s.signInURL())
	})
}

func isOpenAsset(p string) bool {
	return p == "/healthz" || p == "/manifest.webmanifest" || p == "/sw.js" || strings.HasPrefix(p, "/static/")
}

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
	// Secure only over HTTPS (direct or proxied); plain HTTP is allowed for home LANs.
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

func (s *Server) view(r *http.Request, title string, data any) view {
	id := identityFrom(r.Context())
	v := view{Title: title, Data: data, Version: s.version(), ExternalSignIn: s.SignIn != nil}
	if r.Method == http.MethodGet {
		v.Edit = r.URL.Query().Get("edit")
	}
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

func actor(r *http.Request) *auth.Session {
	return identityFrom(r.Context()).Session
}
