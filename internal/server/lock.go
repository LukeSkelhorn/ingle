package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/lock"
)

type lockView struct {
	Locked  bool
	Since   string
	CanLock bool
}

type lockedPage struct {
	Clock      clockView
	Adults     []unlockAdult
	HasCode    bool
	CodeLength int
}

func (s *Server) locked(r *http.Request) (lock.State, error) {
	if s.Lock == nil {
		return lock.State{}, nil
	}
	return s.Lock.State(r.Context(), hhOf(r))
}

func (s *Server) lockGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identityFrom(r.Context())
		if id.Device == nil || s.Lock == nil {
			next.ServeHTTP(w, r)
			return
		}
		st, err := s.locked(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !st.Locked {
			// Automatic polling (clock, screensaver, event stream) isn't activity.
			if r.Method != http.MethodGet || r.URL.Path == "/" {
				s.Lock.Touch(hhOf(r))
			}
			next.ServeHTTP(w, r)
			return
		}
		if allowedWhileLocked(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/")
		}
		http.Error(w, "The displays are locked.", http.StatusLocked)
	})
}

func allowedWhileLocked(r *http.Request) bool {
	p := r.URL.Path
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return p == "/" || p == "/clock" || p == "/screensaver/photos" || p == "/board/events" ||
			strings.HasPrefix(p, "/photos/")
	case http.MethodPost:
		return p == "/unlock" || p == "/lock"
	}
	return false
}

func (s *Server) boardLock(r *http.Request) (lockView, error) {
	if s.Lock == nil {
		return lockView{}, nil
	}
	st, err := s.locked(r)
	if err != nil {
		return lockView{}, err
	}
	v := lockView{Locked: st.Locked}
	if st.Locked {
		loc := s.Locale
		loc.Location = s.Settings.Location(r.Context(), hhOf(r))
		v.Since = loc.FormatTime(loc.In(st.Since))
	}
	if identityFrom(r.Context()).Device == nil {
		v.CanLock = true
	} else if v.CanLock, err = s.Auth.CanUnlockOnDisplay(r.Context(), hhOf(r)); err != nil {
		return lockView{}, err
	}
	return v, nil
}

func (s *Server) renderLocked(w http.ResponseWriter, r *http.Request) {
	adults, err := s.unlockAdults(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.view(r, "", lockedPage{Clock: s.clock(r.Context()), Adults: adults, HasCode: set.HasUnlockCode(), CodeLength: set.UnlockCodeLength})
	v.Household = ""
	s.render(w, r, http.StatusOK, "locked", v)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	if s.Lock == nil {
		http.NotFound(w, r)
		return
	}
	id := identityFrom(r.Context())
	reason, by := lock.Manual, (*int64)(nil)
	if id.Session != nil {
		by = &id.Session.Person.ID
	}
	if id.Device == nil {
		reason = lock.Remote
	} else {
		ok, err := s.Auth.CanUnlockOnDisplay(r.Context(), hhOf(r))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			http.Error(w, "Set a household unlock code or an adult PIN first, so the display can be unlocked again.", http.StatusConflict)
			return
		}
	}
	if err := s.Lock.Lock(r.Context(), hhOf(r), by, reason); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, backTo(r))
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if s.Lock == nil {
		http.NotFound(w, r)
		return
	}
	id := identityFrom(r.Context())
	if id.Device == nil {
		if id.Session == nil {
			redirect(w, r, s.signInURL())
			return
		}
		if err := s.Lock.Unlock(r.Context(), hhOf(r), &id.Session.Person.ID, lock.ByPhone); err != nil {
			s.serverError(w, r, err)
			return
		}
		redirect(w, r, backTo(r))
		return
	}

	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var who *int64
	var err error
	if pid, perr := strconv.ParseInt(r.PostFormValue("person"), 10, 64); perr == nil {
		err = s.Auth.CheckUnlockPIN(r.Context(), id.Device, pid, r.PostFormValue("code"), s.clientAddr(r))
		who = &pid
	} else {
		who, err = s.Auth.CheckUnlockCode(r.Context(), id.Device, r.PostFormValue("code"), s.clientAddr(r))
	}
	switch {
	case errors.Is(err, auth.ErrLockedOut):
		keypadError(w, http.StatusTooManyRequests, "Too many tries. Try again in 5 minutes.")
		return
	case errors.Is(err, auth.ErrBadCredentials):
		msg := "Wrong code. Try again."
		if r.PostFormValue("person") != "" {
			msg = "Wrong PIN. Try again."
		}
		keypadError(w, http.StatusUnauthorized, msg)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	how := lock.ByCode
	if who != nil {
		how = lock.ByPIN
	}
	if err := s.Lock.Unlock(r.Context(), hhOf(r), who, how); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func keypadError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handleActivity(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func backTo(r *http.Request) string {
	if r.PostFormValue("next") == "/admin" {
		return "/admin"
	}
	return "/"
}

func (s *Server) notWhileLocked(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if identityFrom(r.Context()).Device != nil {
			st, err := s.locked(r)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			if st.Locked {
				redirect(w, r, "/")
				return
			}
		}
		next(w, r)
	}
}
