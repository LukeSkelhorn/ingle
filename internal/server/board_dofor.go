package server

import (
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// doForView is the sheet a long press on a child's chore opens: an adult
// can say they did it, and get the points themselves.
type doForView struct {
	OccurrenceID int64
	Title, Emoji string
	Child, Adult dbq.Person
	Points       int64 // what the adult earns
}

// handleDoForSheet is the "I did it" sheet. It needs an adult, signed in or
// unlocked, like approving a chore.
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
	points, _ := chores.Earns(adult.Kind, ch.Points, ch.Pence, ch.AdultPoints)
	s.renderPartial(w, r, "do_for", doForView{
		OccurrenceID: id, Title: ch.Title, Emoji: ch.Emoji,
		Child: child, Adult: *adult, Points: points,
	})
}

// handleDoFor records the adult doing the child's chore. The adult is
// checked here, not just by hiding the sheet.
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
	c, err := s.Chores.DoFor(r.Context(), hhOf(r), id, *child, adult.ID)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{
		Message: "Thanks, " + adult.Name + "! Done for " + s.personName(r, *child) + ".",
		Undo:    "/board/completions/" + itoa(c.ID) + "/undo",
	})
}

// sheetFailed answers a request for a sheet that can't open: an error toast
// and the fresh board, swapped in place of the board rather than into the
// sheet, so no sheet opens.
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
