package server

import (
	"errors"
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// moneyPinView is the PIN pad that opens someone's My money.
type moneyPinView struct {
	Person dbq.Person
	// NotDisplay: My money only opens on a household display.
	NotDisplay bool
	// Expired: their last PIN check ran out, so it's asking again.
	Expired bool
}

// myMoneyView is someone's My money, opened with their PIN. Token re-opens it
// (for the board's refresh) until it runs out.
type myMoneyView struct {
	chores.Summary
	Token string
}

// handleMoneyPin is the PIN pad for one person's My money.
func (s *Server) handleMoneyPin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	s.renderMoneyPin(w, r, p, false)
}

// handleMoneyCheck checks the PIN typed on the pad and, if it's right, shows
// their My money. Errors are JSON for the pad, as for unlocking.
func (s *Server) handleMoneyCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token, err := s.Auth.CheckMoneyPIN(r.Context(), identityFrom(r.Context()).Device, p.ID, r.PostFormValue("pin"), s.clientAddr(r))
	switch {
	case errors.Is(err, auth.ErrLockedOut):
		keypadError(w, http.StatusTooManyRequests, "Too many tries. Try again in 5 minutes.")
		return
	case errors.Is(err, auth.ErrBadCredentials):
		keypadError(w, http.StatusUnauthorized, "Wrong PIN. Try again.")
		return
	case errors.Is(err, auth.ErrNotDisplay):
		keypadError(w, http.StatusForbidden, "My money opens on a household display.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.renderMyMoney(w, r, p, token)
}

// handleMyMoney re-opens My money while the PIN check is still good, and
// asks for the PIN again once it isn't.
func (s *Server) handleMyMoney(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	token := r.URL.Query().Get("t")
	if who, ok := s.Auth.MoneyToken(identityFrom(r.Context()).Device, token); !ok || who != p.ID {
		s.renderMoneyPin(w, r, p, true)
		return
	}
	s.renderMyMoney(w, r, p, token)
}

// moneyPerson is the person in the path, if they're on the board.
func (s *Server) moneyPerson(w http.ResponseWriter, r *http.Request) (dbq.Person, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return dbq.Person{}, false
	}
	p, err := s.Household.Person(r.Context(), s.HouseholdID, id)
	if errors.Is(err, household.ErrNotFound) || (err == nil && p.ArchivedAt != nil) {
		http.NotFound(w, r)
		return dbq.Person{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return dbq.Person{}, false
	}
	return p, true
}

func (s *Server) renderMoneyPin(w http.ResponseWriter, r *http.Request, p dbq.Person, expired bool) {
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "money_pin", moneyPinView{
		Person: p, NotDisplay: identityFrom(r.Context()).Device == nil, Expired: expired,
	})
}

func (s *Server) renderMyMoney(w http.ResponseWriter, r *http.Request, p dbq.Person, token string) {
	ctx := r.Context()
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, s.HouseholdID)
	sum, err := s.Chores.PersonSummary(ctx, s.HouseholdID, p.ID, loc.StartOfWeek(s.now()))
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "my_money", myMoneyView{Summary: sum, Token: token})
}
