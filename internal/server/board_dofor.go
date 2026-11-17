package server

import (
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type doForView struct {
	OccurrenceID int64
	Title, Emoji string
	Child, Adult dbq.Person
	Adults       []dbq.Person
	Points       int64
}

func (s *Server) handleDoForSheet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	adult := boardAdult(r)
	if adult == nil {
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
		if p.Kind == "adult" {
			adults = append(adults, p)
		}
	}
	points, _ := chores.Earns(adult.Kind, ch.Points, ch.Pence, ch.AdultPoints)
	s.renderPartial(w, r, "do_for", doForView{
		OccurrenceID: id, Title: ch.Title, Emoji: ch.Emoji,
		Child: child, Adult: *adult, Adults: adults, Points: points,
	})
}

func (s *Server) handleDoFor(w http.ResponseWriter, r *http.Request) {
	id, child, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		setToast(w, toast{Message: needAdult, Error: true})
		s.renderBoard(w, r, http.StatusForbidden)
		return
	}
	if child == nil {
		s.boardFailed(w, r, "Choose whose chore it is.")
		return
	}
	by := adult.ID
	if chosen := newForm(r).optID("by"); chosen != nil {
		by = *chosen
	}
	c, err := s.Chores.DoFor(r.Context(), hhOf(r), id, *child, by)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{
		Message: "Thanks, " + s.personName(r, by) + "! Done for " + s.personName(r, *child) + ".",
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
