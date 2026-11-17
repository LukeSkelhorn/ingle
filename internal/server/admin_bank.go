package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

const bankHistoryDays = 30

type bankPage struct {
	Waiting   []chores.Transfer
	Recent    []chores.Transfer
	Declining int64
	Reason    string
}

func (s *Server) handleBank(w http.ResponseWriter, r *http.Request) {
	s.renderBank(w, r, http.StatusOK, bankPage{}, nil, "")
}

func (s *Server) renderBank(w http.ResponseWriter, r *http.Request, status int, p bankPage, errs valid.Errors, flash string) {
	all, err := s.Chores.Transfers(r.Context(), hhOf(r), s.now().AddDate(0, 0, -bankHistoryDays))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Waiting, p.Recent = nil, nil
	for _, t := range all {
		if t.Pending() {
			p.Waiting = append([]chores.Transfer{t}, p.Waiting...)
		} else {
			p.Recent = append(p.Recent, t)
		}
	}
	v := s.view(r, "Bank", p)
	v.Nav = "bank"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_bank", v)
}

func (s *Server) handleBankApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.Chores.ApproveTransfer(r.Context(), hhOf(r), id, adult.ID); err != nil {
		s.bankFailed(w, r, err, bankPage{})
		return
	}
	s.renderBank(w, r, http.StatusOK, bankPage{}, nil, "Approved. It's come off their balance in Ingle.")
}

func (s *Server) handleBankDecline(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
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
	reason := r.PostFormValue("reason")
	if err := s.Chores.DeclineTransfer(r.Context(), hhOf(r), id, adult.ID, reason); err != nil {
		s.bankFailed(w, r, err, bankPage{Declining: id, Reason: reason})
		return
	}
	s.renderBank(w, r, http.StatusOK, bankPage{}, nil, "Declined. The money's free to spend again.")
}

func (s *Server) bankFailed(w http.ResponseWriter, r *http.Request, err error, p bankPage) {
	if errs, ok := validationErrors(err); ok {
		s.renderBank(w, r, http.StatusUnprocessableEntity, p, errs, "")
		return
	}
	switch {
	case errors.Is(err, chores.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, chores.ErrNotPending):
		s.renderBank(w, r, http.StatusConflict, bankPage{}, nil, "That's already been decided.")
	case errors.Is(err, chores.ErrNotAdult):
		s.renderBank(w, r, http.StatusForbidden, bankPage{}, nil, "Only adults can do that.")
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) handlePayOut(w http.ResponseWriter, r *http.Request) {
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
	typed, all := r.PostFormValue("amount"), r.PostFormValue("all") == "1"
	again := func(status int, msg string) {
		pf := s.personFormFor(r, p)
		pf.Points.PayOut, pf.Points.PayAll = typed, all
		s.renderPerson(w, r, status, pf, valid.Errors{"payout": msg}, "")
	}
	var pence int64
	if all {
		f := s.personFormFor(r, p)
		if err := s.loadPointsPanel(r, &f); err != nil {
			s.serverError(w, r, err)
			return
		}
		pence = f.Points.Pence
	} else {
		var err error
		if pence, err = s.Locale.ParseMoney(typed); err != nil || pence <= 0 {
			again(http.StatusUnprocessableEntity, "Enter an amount like 5.00.")
			return
		}
	}
	_, err := s.Chores.PayOut(r.Context(), hhOf(r), p.ID, pence, adult.ID)
	switch {
	case errors.Is(err, chores.ErrInsufficientBalance):
		again(http.StatusUnprocessableEntity, "That's more than "+p.Name+" has to spend.")
		return
	case errors.Is(err, chores.ErrNotFound):
		s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, p), nil, "Restore them before moving their money.")
		return
	case errors.Is(err, chores.ErrNotAdult):
		s.renderPerson(w, r, http.StatusForbidden, s.personFormFor(r, p), nil, "Only adults can move money.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil,
		fmt.Sprintf("Moved %s to %s's bank.", s.Locale.FormatMoney(pence), p.Name))
}
