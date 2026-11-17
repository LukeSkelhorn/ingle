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

type moneyPinView struct {
	Person     dbq.Person
	NotDisplay bool
	Expired    bool
}

type myMoneyView struct {
	chores.Summary
	Waiting  []chores.HistoryItem
	Bank     chores.BankStatus
	InBank   *chores.Bank
	Page     moneyHistoryView
	Admin    bool
	Interest *moneyInterest
}

type moneyInterest struct {
	SoFar  string
	Saving string
}

func (s *Server) newMoneyInterest(in chores.Interest, sum chores.Summary, soFarPoints, soFarPence int64) *moneyInterest {
	and := func(points, pence int64) string {
		var parts []string
		if pence > 0 {
			parts = append(parts, s.Locale.FormatMoney(pence))
		}
		if points > 0 {
			parts = append(parts, pts(points))
		}
		return strings.Join(parts, " and ")
	}
	mi := &moneyInterest{SoFar: and(soFarPoints, soFarPence)}
	var nowPoints, nowPence, thenPoints, thenPence int64
	if in.Points && sum.Points > 0 {
		nowPoints, thenPoints = sum.Points, chores.InterestAfter(sum.Points, in.RateBP, in.CapPoints, 12)
	}
	if in.Money && sum.Pence > 0 {
		nowPence, thenPence = sum.Pence, chores.InterestAfter(sum.Pence, in.RateBP, in.CapPence, 12)
	}
	if thenPoints > nowPoints || thenPence > nowPence {
		mi.Saving = "If you keep saving " + and(nowPoints, nowPence) + ", you'll have about " +
			and(thenPoints, thenPence) + " this time next year."
	}
	return mi
}

const minTransfer = 50

const declinedShown = 7 * 24 * time.Hour

// Asking reports a child in their own My money on a display, who can ask to move money to their bank.
func (v myMoneyView) Asking() bool { return !v.Admin && v.Page.Token != "" }

// CanAsk reports whether the Ask to move to my bank button shows.
func (v myMoneyView) CanAsk() bool {
	return v.Asking() && v.Bank.Waiting == nil && v.Pence >= minTransfer
}

// BankURL opens the ask panel.
func (v myMoneyView) BankURL() string {
	return v.Page.Base + "/bank?" + url.Values{"t": {v.Page.Token}}.Encode()
}

type moneyBankView struct {
	Person    dbq.Person
	Available int64
	Start     int64
	Step      int64
	Symbol    string
	Base      string
	Token     string
}

// BackURL goes back to My money.
func (v moneyBankView) BackURL() string {
	return moneyHistoryView{Base: v.Base, Token: v.Token}.RefreshURL()
}

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

const moneyHistoryDays = 30

func (s *Server) handleMoneyPin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return
	}
	if moneyAdult(r) {
		s.renderMyMoney(w, r, p, "")
		return
	}
	s.renderMoneyPin(w, r, p, false)
}

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
	if _, err := s.Chores.RequestTransfer(r.Context(), hhOf(r), p.ID, pence); err != nil {
		msg, known := boardErrorText(err)
		if !known {
			s.serverError(w, r, err)
			return
		}
		setToast(w, toast{Message: msg, Error: true})
	} else {
		s.events.publish(hhOf(r), boardClient(r.Header.Get(boardClientHeader)))
		setToast(w, toast{Message: "Asked. A grown-up will move it to your bank."})
	}
	s.renderMyMoney(w, r, p, token)
}

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
	switch err := s.Chores.CancelTransfer(r.Context(), hhOf(r), p.ID, id); {
	case errors.Is(err, chores.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, chores.ErrNotPending):
		setToast(w, toast{Message: "A grown-up has already decided.", Error: true})
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		s.events.publish(hhOf(r), boardClient(r.Header.Get(boardClientHeader)))
		setToast(w, toast{Message: "Cancelled. The money's yours to spend again."})
	}
	s.renderMyMoney(w, r, p, token)
}

func (s *Server) moneyAccess(r *http.Request, p dbq.Person) (token string, ok bool) {
	if moneyAdult(r) {
		return "", true
	}
	token = r.FormValue("t")
	who, ok := s.Auth.MoneyToken(identityFrom(r.Context()).Device, token)
	return token, ok && who == p.ID
}

// moneyAdult excludes displays: there each person types their own PIN, even with admin unlocked.
func moneyAdult(r *http.Request) bool {
	id := identityFrom(r.Context())
	return id.Session != nil && id.Device == nil
}

func boardMoneyBase(personID int64) string {
	return "/board/money/" + strconv.FormatInt(personID, 10)
}

func (s *Server) moneyPerson(w http.ResponseWriter, r *http.Request) (dbq.Person, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return dbq.Person{}, false
	}
	p, err := s.Household.Person(r.Context(), hhOf(r), id)
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

func (s *Server) myMoney(r *http.Request, p dbq.Person, base, token string) (myMoneyView, error) {
	ctx := r.Context()
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, hhOf(r))
	sum, err := s.Chores.PersonSummary(ctx, hhOf(r), p.ID, loc.StartOfWeek(s.now()))
	if err != nil {
		return myMoneyView{}, err
	}
	v := myMoneyView{Summary: sum, Page: moneyHistoryView{Base: base, Token: token}}
	if s.moduleOn(ctx, modRewards) {
		if v.Waiting, err = s.Chores.WaitingClaims(ctx, hhOf(r), p.ID); err != nil {
			return myMoneyView{}, err
		}
	} else {
		v.Goal = nil
	}
	if v.Bank, err = s.Chores.BankStatus(ctx, hhOf(r), p.ID, s.now().Add(-declinedShown)); err != nil {
		return myMoneyView{}, err
	}
	if v.InBank, err = s.Chores.PersonBank(ctx, hhOf(r), p.ID); err != nil {
		return myMoneyView{}, err
	}
	in, err := s.Chores.InterestSettings(ctx, hhOf(r))
	if err != nil {
		return myMoneyView{}, err
	}
	if in.Earns(sum.Person) {
		points, pence, err := s.Chores.InterestTotals(ctx, hhOf(r), p.ID)
		if err != nil {
			return myMoneyView{}, err
		}
		v.Interest = s.newMoneyInterest(in, sum, points, pence)
	}
	v.Page.History, v.Page.Next, err = s.Chores.History(ctx, hhOf(r), p.ID, s.now().AddDate(0, 0, -moneyHistoryDays), 0)
	return v, err
}

func (s *Server) renderMoneyHistory(w http.ResponseWriter, r *http.Request, p dbq.Person, base, token string) {
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		http.Error(w, "bad before", http.StatusBadRequest)
		return
	}
	v := moneyHistoryView{Base: base, Token: token}
	v.History, v.Next, err = s.Chores.History(r.Context(), hhOf(r), p.ID, time.Time{}, before)
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
