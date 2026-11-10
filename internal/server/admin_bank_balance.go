package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// bankPanel is the person page's Bank balance section: what Ingle knows is
// in a child's real bank, and the form to update it (with what was typed,
// after an error).
type bankPanel struct {
	Current *chores.Bank // nil until it's set up
	Label   string
	Amount  string
	Typed   bool // Label and Amount are what was typed, not Current
}

// loadBankPanel fills f.Bank for a child while My money is on.
func (s *Server) loadBankPanel(r *http.Request, f *personForm) error {
	if f.Kind != "child" || !s.moduleOn(r.Context(), modMoney) {
		return nil
	}
	b, err := s.Chores.PersonBank(r.Context(), hhOf(r), f.ID)
	if err != nil {
		return err
	}
	f.Bank.Current = b
	if b != nil && !f.Bank.Typed {
		f.Bank.Label, f.Bank.Amount = b.Label, formatMoneyInput(b.Pence)
	}
	return nil
}

// handleSetBankBalance is Update on the Bank balance section: an adult has
// looked at the child's real bank and types in what's there.
func (s *Server) handleSetBankBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	label, amount := r.PostFormValue("bank_label"), r.PostFormValue("bank_amount")
	again := func(status int, errs valid.Errors) {
		f := s.personFormFor(r, p)
		f.Bank = bankPanel{Label: label, Amount: amount, Typed: true}
		s.renderPerson(w, r, status, f, errs, "")
	}
	pence, err := s.Locale.ParseMoney(amount)
	if err != nil || strings.TrimSpace(amount) == "" {
		again(http.StatusUnprocessableEntity, valid.Errors{"bank_pence": "Enter the balance, like 23.40."})
		return
	}
	err = s.Chores.SetBank(r.Context(), hhOf(r), p.ID, label, pence, adult.ID)
	if errs, ok := validationErrors(err); ok {
		again(http.StatusUnprocessableEntity, errs)
		return
	}
	if err != nil {
		s.bankBalanceFailed(w, r, p.Name, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil,
		fmt.Sprintf("Updated %s's bank balance.", p.Name))
}

// handleRemoveBankBalance stops showing a child's bank balance.
func (s *Server) handleRemoveBankBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	err := s.Chores.RemoveBank(r.Context(), hhOf(r), p.ID, adult.ID)
	if errors.Is(err, chores.ErrNotFound) {
		s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil, "")
		return
	}
	if err != nil {
		s.bankBalanceFailed(w, r, p.Name, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil,
		fmt.Sprintf("Removed %s's bank balance.", p.Name))
}

func (s *Server) bankBalanceFailed(w http.ResponseWriter, r *http.Request, name string, err error) {
	switch {
	case errors.Is(err, chores.ErrNotChild):
		http.Error(w, "only children have a bank balance", http.StatusBadRequest)
	case errors.Is(err, chores.ErrNotFound): // archived
		http.Error(w, "restore "+name+" first", http.StatusConflict)
	case errors.Is(err, chores.ErrNotAdult):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		s.serverError(w, r, err)
	}
}

// updatedAgo is how long ago a bank balance was checked: "today",
// "yesterday" or "3 days ago".
func updatedAgo(now, t time.Time) string {
	switch d := now.Sub(t); {
	case d < 24*time.Hour:
		return "today"
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
