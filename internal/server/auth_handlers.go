package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

type setupForm struct {
	HouseholdName string
	Name          string
	Email         string
	Colour        string
	Emoji         string
	UseAsDisplay  bool
	DisplayName   string
	Colours       []string
	Emojis        []string
}

func newSetupForm() setupForm {
	return setupForm{
		HouseholdName: "Home",
		Colour:        household.Colours[5],
		UseAsDisplay:  true,
		DisplayName:   "Kitchen",
		Colours:       household.Colours,
		Emojis:        avatar.Emojis,
	}
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if hh := hhOf(r); hh == 0 || s.SignIn != nil || s.isSetUp(hh) {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "setup", s.view(r, "Set up Ingle", newSetupForm()))
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if hh := hhOf(r); hh == 0 || s.SignIn != nil || s.isSetUp(hh) {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	data := newSetupForm()
	data.HouseholdName = f.str("household_name")
	data.Name = f.str("name")
	data.Email = f.str("email")
	data.Colour = f.str("colour")
	data.Emoji = f.str("emoji")
	data.UseAsDisplay = f.checked("use_as_display")
	data.DisplayName = f.str("display_name")

	var av *string
	if data.Emoji != "" {
		if v, ok := avatar.EmojiValue(data.Emoji); ok {
			av = &v
		}
	}
	if f.raw("password") != f.raw("password_confirm") {
		f.errs.Add("password_confirm", "The passwords don't match.")
	}
	if f.str("pin") != f.str("pin_confirm") {
		f.errs.Add("pin_confirm", "The PINs don't match.")
	}
	if data.UseAsDisplay && (data.DisplayName == "" || len([]rune(data.DisplayName)) > 50) {
		f.errs.Add("display_name", "Name this display, e.g. Kitchen.")
	}
	rerender := func(errs valid.Errors) {
		v := s.view(r, "Set up Ingle", data)
		v.Errors = errs
		s.render(w, r, http.StatusUnprocessableEntity, "setup", v)
	}
	if !f.ok() {
		rerender(f.errs)
		return
	}

	ctx := r.Context()
	p, err := s.Household.Setup(ctx, hhOf(r), household.SetupInput{
		HouseholdName: data.HouseholdName,
		Person:        household.PersonInput{Name: data.Name, Colour: data.Colour, Avatar: av},
		Email:         data.Email,
		Password:      f.raw("password"),
		PIN:           f.str("pin"),
	})
	if errs, ok := validationErrors(err); ok {
		rerender(errs)
		return
	}
	if errors.Is(err, household.ErrSetupDone) {
		s.markSetUp(hhOf(r))
		redirect(w, r, "/")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.markSetUp(hhOf(r))
	s.Settings.Invalidate(hhOf(r))
	if err := s.Modules.SetDefaults(ctx, hhOf(r), &p.ID); err != nil {
		s.Log.ErrorContext(ctx, "set default modules", slog.Any("err", err))
	}

	if data.UseAsDisplay {
		devToken, dev, err := s.Auth.RegisterDisplay(ctx, hhOf(r), data.DisplayName, p.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.setCookie(w, r, deviceCookie, devToken, deviceCookieAge)
		token, err := s.Auth.UnlockWithPIN(ctx, &dev, p.ID, f.str("pin"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.setCookie(w, r, sessionCookie, token, 0)
	} else {
		token, err := s.Auth.StartPersonalSession(ctx, p, r.UserAgent())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.setCookie(w, r, sessionCookie, token, s.Auth.PersonalIdle())
	}
	redirect(w, r, "/admin")
}

type signInForm struct {
	Email string
}

func (s *Server) handleSignInForm(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	switch {
	case id.Session != nil:
		redirect(w, r, "/admin")
		return
	case id.Device != nil:
		redirect(w, r, "/admin/unlock")
		return
	case s.SignIn != nil:
		redirect(w, r, s.SignIn.URL)
		return
	}
	s.render(w, r, http.StatusOK, "signin", s.view(r, "Sign in", signInForm{}))
}

func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if s.SignIn != nil {
		http.NotFound(w, r)
		return
	}
	if identityFrom(r.Context()).Device != nil {
		redirect(w, r, "/admin/unlock")
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	data := signInForm{Email: f.str("email")}
	token, p, err := s.Auth.SignIn(r.Context(), data.Email, f.raw("password"), r.UserAgent(), s.clientAddr(r))
	if hh := hhOf(r); err == nil && hh != 0 && p.HouseholdID != hh {
		// Same answer as a wrong password, so other households aren't revealed.
		if endErr := s.Auth.EndSession(r.Context(), token); endErr != nil {
			s.serverError(w, r, endErr)
			return
		}
		err = auth.ErrBadCredentials
	}
	if err != nil {
		v := s.view(r, "Sign in", data)
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			v.Flash = "Too many attempts. Wait five minutes and try again."
		case errors.Is(err, auth.ErrBadCredentials):
			v.Flash = "That email and password don't match."
		default:
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, http.StatusUnauthorized, "signin", v)
		return
	}
	s.setCookie(w, r, sessionCookie, token, s.Auth.PersonalIdle())
	redirect(w, r, "/admin")
}

// StartSession signs an active adult of hh in with a personal session and sets its cookie.
func (s *Server) StartSession(w http.ResponseWriter, r *http.Request, hh, personID int64) error {
	if cur := hhOf(r); cur != 0 && cur != hh {
		return auth.ErrBadCredentials
	}
	token, err := s.Auth.StartSessionFor(r.Context(), hh, personID, r.UserAgent())
	if err != nil {
		return err
	}
	s.setCookie(w, r, sessionCookie, token, s.Auth.PersonalIdle())
	return nil
}

// RegisterDisplay makes the browser of r a household display of hh, named name, added by adult by.
func (s *Server) RegisterDisplay(w http.ResponseWriter, r *http.Request, hh int64, name string, by int64) error {
	if cur := hhOf(r); cur != 0 && cur != hh {
		return auth.ErrBadCredentials
	}
	ctx := withHousehold(r.Context(), hh)
	p, err := s.Store.Q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: hh, ID: by})
	if errors.Is(err, sql.ErrNoRows) || err == nil && (p.Kind != "adult" || p.ArchivedAt != nil) {
		return auth.ErrBadCredentials
	}
	if err != nil {
		return fmt.Errorf("get person: %w", err)
	}
	token, _, err := s.Auth.RegisterDisplay(ctx, hh, name, by)
	if err != nil {
		return err
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Auth.EndSession(ctx, c.Value)
	}
	s.clearCookie(w, r, sessionCookie)
	s.setCookie(w, r, deviceCookie, token, deviceCookieAge)
	return nil
}

// CreateHousehold makes a household and its first adult, with each module's default on.
func (s *Server) CreateHousehold(ctx context.Context, in household.NewHouseholdInput) (hh, personID int64, err error) {
	h, p, err := s.Household.CreateHousehold(ctx, in)
	if err != nil {
		return 0, 0, err
	}
	ctx = withHousehold(ctx, h.ID)
	s.Settings.Invalidate(h.ID)
	if err := s.Modules.SetDefaults(ctx, h.ID, &p.ID); err != nil {
		s.Log.ErrorContext(ctx, "set default modules", slog.Any("err", err))
	}
	return h.ID, p.ID, nil
}

// SetPIN sets the PIN of the adult signed in on r, ending their other sessions.
func (s *Server) SetPIN(r *http.Request, pin string) error {
	sess := identityFrom(r.Context()).Session
	if sess == nil {
		return errors.New("server: SetPIN: nobody is signed in")
	}
	hh := sess.Person.HouseholdID
	return s.Household.SetPIN(withHousehold(r.Context(), hh), hh, sess.Person.ID, sess.Person.ID, pin, sess.TokenHash)
}

// AdultOf returns the adult signed in on r, if any.
func AdultOf(r *http.Request) (personID int64, ok bool) {
	if sess := identityFrom(r.Context()).Session; sess != nil {
		return sess.Person.ID, true
	}
	return 0, false
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Auth.EndSession(r.Context(), c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.clearCookie(w, r, sessionCookie)
	if identityFrom(r.Context()).Device != nil {
		redirect(w, r, "/")
		return
	}
	if s.SignIn != nil && s.SignIn.OnSignOut != nil {
		if next := s.SignIn.OnSignOut(r); next != "" {
			redirect(w, r, next)
			return
		}
	}
	redirect(w, r, s.signInURL())
}

type unlockView struct {
	Adults   []unlockAdult
	Selected *unlockAdult
}

type unlockAdult struct {
	Person dbq.Person
	Locked bool
}

func (s *Server) unlockAdults(r *http.Request) ([]unlockAdult, error) {
	people, err := s.Household.People(r.Context(), hhOf(r))
	if err != nil {
		return nil, err
	}
	var out []unlockAdult
	for _, p := range people {
		if p.Kind == household.KindAdult && p.PinHash != nil {
			out = append(out, unlockAdult{Person: p, Locked: s.Auth.PINLocked(p.ID)})
		}
	}
	return out, nil
}

func (s *Server) handleUnlockForm(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	if id.Device == nil {
		redirect(w, r, s.signInURL())
		return
	}
	if id.Session != nil {
		redirect(w, r, "/admin")
		return
	}
	adults, err := s.unlockAdults(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := unlockView{Adults: adults}
	if pid, err := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64); err == nil {
		for i := range adults {
			if adults[i].Person.ID == pid {
				data.Selected = &adults[i]
			}
		}
	}
	s.render(w, r, http.StatusOK, "unlock", s.view(r, "Unlock admin", data))
}

func (s *Server) handleAdminUnlock(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	if id.Device == nil {
		redirect(w, r, s.signInURL())
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	pid := f.optID("person")
	if pid == nil {
		redirect(w, r, "/admin/unlock")
		return
	}
	token, err := s.Auth.UnlockWithPIN(r.Context(), id.Device, *pid, f.str("pin"))
	if err != nil {
		adults, lerr := s.unlockAdults(r)
		if lerr != nil {
			s.serverError(w, r, lerr)
			return
		}
		data := unlockView{Adults: adults}
		for i := range adults {
			if adults[i].Person.ID == *pid {
				data.Selected = &adults[i]
			}
		}
		v := s.view(r, "Unlock admin", data)
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			v.Flash = "Too many wrong PINs. Try again in five minutes."
		case errors.Is(err, auth.ErrBadCredentials):
			v.Flash = "Wrong PIN. Try again."
		default:
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, http.StatusUnauthorized, "unlock", v)
		return
	}
	s.setCookie(w, r, sessionCookie, token, 0)
	redirect(w, r, "/admin")
}

// clientAddr trusts X-Forwarded-For only from INGLE_TRUSTED_PROXIES, since it can be forged.
func (s *Server) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !s.trusted(peer) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a mangled hop: trust nothing to its left
		}
		if !s.trusted(a) {
			return a.Unmap().String()
		}
	}
	return host
}

func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
