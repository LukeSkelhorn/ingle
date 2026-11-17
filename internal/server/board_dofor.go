package server

import (
	"errors"
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type doForView struct {
	OccurrenceID int64
	Title, Emoji string
	Child, Adult dbq.Person
	Adults       []dbq.Person
	Points       int64
	NeedPIN      bool
}

func (s *Server) handleDoForSheet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	adult := boardAdult(r)
	needPIN := adult == nil
	if needPIN && identityFrom(r.Context()).Device == nil {
		s.sheetFailed(w, r, chores.ErrNotAdult)
		return
	}
	childID := newForm(r).optID("person")
	if childID == nil {
		s.sheetFailed(w, r, chores.ErrNotFound)
		return
	}
	ctx, hh := r.Context(), hhOf(r)
	child, err := s.Store.Q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: hh, ID: *childID})
	if err != nil || child.ArchivedAt != nil {
		s.sheetFailed(w, r, chores.ErrNotFound)
		return
	}
	if child.Kind != "child" {
		s.sheetFailed(w, r, chores.ErrNotChild)
		return
	}
	occ, err := s.Store.Q.GetOccurrence(ctx, dbq.GetOccurrenceParams{HouseholdID: hh, ID: id})
	if err != nil {
		s.sheetFailed(w, r, chores.ErrNotFound)
		return
	}
	if occ.Status == chores.StatusDone {
		s.sheetFailed(w, r, chores.ErrNotOpen)
		return
	}
	ch, err := s.Store.Q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: hh, ID: occ.ChoreID})
	if err != nil {
		s.sheetFailed(w, r, chores.ErrNotFound)
		return
	}
	people, err := s.Store.Q.ListPeople(ctx, hh)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var adults []dbq.Person
	for _, p := range people {
		if p.Kind == "adult" && (!needPIN || p.PinHash != nil) {
			adults = append(adults, p)
		}
	}
	v := doForView{
		OccurrenceID: id, Title: ch.Title, Emoji: ch.Emoji,
		Child: child, Adults: adults, NeedPIN: needPIN,
	}
	if adult != nil {
		v.Adult = *adult
	}
	v.Points, _ = chores.Earns("adult", ch.Points, ch.Pence, ch.AdultPoints)
	s.renderPartial(w, r, "do_for", v)
}

func (s *Server) handleDoFor(w http.ResponseWriter, r *http.Request) {
	id, child, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	chosen := newForm(r).optID("by")
	adult := boardAdult(r)
	if adult == nil {
		device := identityFrom(r.Context()).Device
		if device == nil {
			setToast(w, toast{Message: needAdult, Error: true})
			s.renderBoard(w, r, http.StatusForbidden)
			return
		}
		if chosen == nil {
			keypadError(w, http.StatusBadRequest, "Choose who did it.")
			return
		}
		err := s.Auth.CheckUnlockPIN(r.Context(), device, *chosen, r.PostFormValue("pin"), s.clientAddr(r))
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			keypadError(w, http.StatusTooManyRequests, "Too many tries. Try again in 5 minutes.")
			return
		case errors.Is(err, auth.ErrBadCredentials):
			keypadError(w, http.StatusUnauthorized, "Wrong PIN. Try again.")
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
	}
	if child == nil {
		s.boardFailed(w, r, "Choose whose chore it is.")
		return
	}
	by := chosen
	if by == nil {
		by = &adult.ID
	}
	c, err := s.Chores.DoFor(r.Context(), hhOf(r), id, *child, *by)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{
		Message: "Thanks, " + s.personName(r, *by) + "! Done for " + s.personName(r, *child) + ".",
		Undo:    "/board/completions/" + itoa(c.ID) + "/undo",
	})
}

func (s *Server) sheetFailed(w http.ResponseWriter, r *http.Request, err error) {
	msg, ok := boardErrorText(err)
	if !ok {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("HX-Retarget", "#board")
	w.Header().Set("HX-Reswap", "outerHTML")
	s.boardFailed(w, r, msg)
}
