package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

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

// myMoneyView is someone's My money: on the board after their PIN (Token
// re-opens it until it runs out), on the board for a signed-in adult (no
// Token), or on their admin page (Admin).
type myMoneyView struct {
	chores.Summary
	Waiting []chores.HistoryItem // reward claims waiting for a grown-up
	Page    moneyHistoryView     // the first page of history
	Admin   bool                 // on Admin › People, not in the board's sheet
}

// moneyHistoryView is a page of someone's money history. Next is where Show
// more carries on from (0 when there's nothing older); Base and Token build
// its URL.
type moneyHistoryView struct {
	History []chores.HistoryItem
	Next    int64
	Base    string
	Token   string
}

// MoreURL is where Show more fetches the next page from.
func (v moneyHistoryView) MoreURL() string {
	q := url.Values{"before": {strconv.FormatInt(v.Next, 10)}}
	if v.Token != "" {
		q.Set("t", v.Token)
	}
	return v.Base + "/history?" + q.Encode()
}

// RefreshURL is where the board's sheet re-fetches My money from.
func (v moneyHistoryView) RefreshURL() string {
	if v.Token == "" {
		return v.Base + "/view"
	}
	return v.Base + "/view?" + url.Values{"t": {v.Token}}.Encode()
}

// moneyHistoryDays is how far back My money's history goes before Show more.
const moneyHistoryDays = 30

// handleMoneyPin is the PIN pad for one person's My money.
func (s *Server) handleMoneyPin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	if moneyAdult(r) {
		// A signed-in adult on a phone can see anyone's money without
		// their PIN.
		s.renderMyMoney(w, r, p, "")
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

// handleMyMoney re-opens My money while the PIN check is still good (or for
// a signed-in adult), and asks for the PIN again once it isn't.
func (s *Server) handleMyMoney(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	token, ok := s.moneyAccess(r, p)
	if !ok {
		s.renderMoneyPin(w, r, p, true)
		return
	}
	s.renderMyMoney(w, r, p, token)
}

// handleMoneyHistory is Show more on the board's My money. Once the PIN
// check has run out the whole sheet goes back to the keypad.
func (s *Server) handleMoneyHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	token, ok := s.moneyAccess(r, p)
	if !ok {
		w.Header().Set("HX-Retarget", "#sheet-body")
		w.Header().Set("HX-Reswap", "innerHTML")
		s.renderMoneyPin(w, r, p, true)
		return
	}
	s.renderMoneyHistory(w, r, p, boardMoneyBase(p.ID), token)
}

// moneyAccess reports whether this request may see p's money: a signed-in
// adult on a phone always can, and a display can with a good token for p.
// A good token is extended.
func (s *Server) moneyAccess(r *http.Request, p dbq.Person) (token string, ok bool) {
	if moneyAdult(r) {
		return "", true
	}
	token = r.URL.Query().Get("t")
	who, ok := s.Auth.MoneyToken(identityFrom(r.Context()).Device, token)
	return token, ok && who == p.ID
}

// moneyAdult reports a signed-in adult off the household displays, who can
// see anyone's My money without their PIN. On a display, even with admin
// unlocked, each person types their own PIN, so a child walking up to it
// can't see their brother's.
func moneyAdult(r *http.Request) bool {
	id := identityFrom(r.Context())
	return id.Session != nil && id.Device == nil
}

func boardMoneyBase(personID int64) string {
	return "/board/money/" + strconv.FormatInt(personID, 10)
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
	v, err := s.myMoney(r, p, boardMoneyBase(p.ID), token)
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "my_money", v)
}

// myMoney loads someone's My money: balance, goal, waiting claims and the
// first page of history.
func (s *Server) myMoney(r *http.Request, p dbq.Person, base, token string) (myMoneyView, error) {
	ctx := r.Context()
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, s.HouseholdID)
	sum, err := s.Chores.PersonSummary(ctx, s.HouseholdID, p.ID, loc.StartOfWeek(s.now()))
	if err != nil {
		return myMoneyView{}, err
	}
	v := myMoneyView{Summary: sum, Page: moneyHistoryView{Base: base, Token: token}}
	if s.moduleOn(ctx, modRewards) {
		if v.Waiting, err = s.Chores.WaitingClaims(ctx, s.HouseholdID, p.ID); err != nil {
			return myMoneyView{}, err
		}
	} else {
		v.Goal = nil
	}
	v.Page.History, v.Page.Next, err = s.Chores.History(ctx, s.HouseholdID, p.ID, s.now().AddDate(0, 0, -moneyHistoryDays), 0)
	return v, err
}

// renderMoneyHistory is the next page of history after ?before=.
func (s *Server) renderMoneyHistory(w http.ResponseWriter, r *http.Request, p dbq.Person, base, token string) {
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		http.Error(w, "bad before", http.StatusBadRequest)
		return
	}
	v := moneyHistoryView{Base: base, Token: token}
	v.History, v.Next, err = s.Chores.History(r.Context(), s.HouseholdID, p.ID, time.Time{}, before)
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "money_history", v)
}

// handleAdminMoney is someone's My money on Admin › People, for adults on
// any device and without the person's PIN.
func (s *Server) handleAdminMoney(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	if p.ArchivedAt != nil {
		http.NotFound(w, r)
		return
	}
	mv, err := s.myMoney(r, p, adminMoneyBase(p.ID), "")
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	mv.Admin = true
	w.Header().Set("Cache-Control", "no-store")
	v := s.view(r, p.Name+"'s money", mv)
	v.Nav = "people"
	s.render(w, r, http.StatusOK, "admin_money", v)
}

// handleAdminMoneyHistory is Show more on the admin My money page.
func (s *Server) handleAdminMoneyHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	s.renderMoneyHistory(w, r, p, adminMoneyBase(p.ID), "")
}

func adminMoneyBase(personID int64) string {
	return "/admin/people/" + strconv.FormatInt(personID, 10) + "/money"
}
