package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	Bank    chores.BankStatus    // their bank request waiting, or just declined
	InBank  *chores.Bank         // what's in their real bank, nil if not set up
	Page    moneyHistoryView     // the first page of history
	Admin   bool                 // on Admin › People, not in the board's sheet
}

// minTransfer is the least a child can ask to move to their bank, and the
// step the amount moves in.
const minTransfer = 50

// declinedShown is how long My money says why a bank request was declined.
const declinedShown = 7 * 24 * time.Hour

// Asking reports a child in their own My money on a display, who can ask
// for money to go to their bank and cancel the request. An adult pays out
// from Admin › People instead.
func (v myMoneyView) Asking() bool { return !v.Admin && v.Page.Token != "" }

// CanAsk reports whether the Ask to move to my bank button shows.
func (v myMoneyView) CanAsk() bool {
	return v.Asking() && v.Bank.Waiting == nil && v.Pence >= minTransfer
}

// BankURL opens the ask panel.
func (v myMoneyView) BankURL() string {
	return v.Page.Base + "/bank?" + url.Values{"t": {v.Page.Token}}.Encode()
}

// moneyBankView is the panel where a child picks how much to ask for.
type moneyBankView struct {
	Person    dbq.Person
	Available int64 // what they can ask for, in pence
	Start     int64 // the amount it starts at
	Step      int64
	Symbol    string // the currency symbol, for the stepper's script
	Base      string
	Token     string
}

// BackURL goes back to My money.
func (v moneyBankView) BackURL() string {
	return moneyHistoryView{Base: v.Base, Token: v.Token}.RefreshURL()
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

// childMoney is a bank request handler's person and PIN check: refused for
// an adult (they pay out from Admin › People), and back to the keypad once
// the check runs out.
func (s *Server) childMoney(w http.ResponseWriter, r *http.Request) (dbq.Person, string, bool) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return p, "", false
	}
	if moneyAdult(r) {
		http.Error(w, "adults move money from Admin › People", http.StatusForbidden)
		return p, "", false
	}
	if r.Method == http.MethodPost {
		if err := parseForm(w, r); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return p, "", false
		}
	}
	token, ok := s.moneyAccess(r, p)
	if !ok {
		w.Header().Set("HX-Retarget", "#sheet-body")
		w.Header().Set("HX-Reswap", "innerHTML")
		s.renderMoneyPin(w, r, p, true)
		return p, "", false
	}
	return p, token, true
}

// handleBankAsk is the panel where a child picks how much to ask for.
func (s *Server) handleBankAsk(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.childMoney(w, r)
	if !ok {
		return
	}
	v, err := s.myMoney(r, p, boardMoneyBase(p.ID), token)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !v.CanAsk() {
		// Already asked, or not enough: My money says which.
		s.renderMyMoney(w, r, p, token)
		return
	}
	avail := v.Pence / minTransfer * minTransfer
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "money_bank", moneyBankView{
		Person: p, Available: avail, Start: min(100, avail), Step: minTransfer,
		Symbol: strings.TrimSuffix(s.Locale.FormatMoney(0), "0.00"),
		Base:   boardMoneyBase(p.ID), Token: token,
	})
}

// handleBankRequest sends a child's request, then shows My money with it
// waiting.
func (s *Server) handleBankRequest(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.childMoney(w, r)
	if !ok {
		return
	}
	pence, err := strconv.ParseInt(r.PostFormValue("pence"), 10, 64)
	if err != nil || pence < minTransfer || pence%minTransfer != 0 {
		http.Error(w, "bad amount", http.StatusBadRequest)
		return
	}
	if _, err := s.Chores.RequestTransfer(r.Context(), s.HouseholdID, p.ID, pence); err != nil {
		msg, known := boardErrorText(err)
		if !known {
			s.serverError(w, r, err)
			return
		}
		setToast(w, toast{Message: msg, Error: true})
	} else {
		s.events.publish(s.HouseholdID, boardClient(r.Header.Get(boardClientHeader)))
		setToast(w, toast{Message: "Asked. A grown-up will move it to your bank."})
	}
	s.renderMyMoney(w, r, p, token)
}

// handleBankCancel is a child cancelling their waiting request.
func (s *Server) handleBankCancel(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.childMoney(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("req"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	switch err := s.Chores.CancelTransfer(r.Context(), s.HouseholdID, p.ID, id); {
	case errors.Is(err, chores.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, chores.ErrNotPending):
		setToast(w, toast{Message: "A grown-up has already decided.", Error: true})
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		s.events.publish(s.HouseholdID, boardClient(r.Header.Get(boardClientHeader)))
		setToast(w, toast{Message: "Cancelled. The money's yours to spend again."})
	}
	s.renderMyMoney(w, r, p, token)
}

// moneyAccess reports whether this request may see p's money: a signed-in
// adult on a phone always can, and a display can with a good token for p
// (in the query, or a posted form). A good token is extended.
func (s *Server) moneyAccess(r *http.Request, p dbq.Person) (token string, ok bool) {
	if moneyAdult(r) {
		return "", true
	}
	token = r.FormValue("t")
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
	if v.Bank, err = s.Chores.BankStatus(ctx, s.HouseholdID, p.ID, s.now().Add(-declinedShown)); err != nil {
		return myMoneyView{}, err
	}
	if v.InBank, err = s.Chores.PersonBank(ctx, s.HouseholdID, p.ID); err != nil {
		return myMoneyView{}, err
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
