package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// adjustmentHistory is how far back the history lists go.
const adjustmentHistory = 30 * 24 * time.Hour

// loadPointsPanel fills in the person page's balance, suggested reasons and
// recent adjustments.
func (s *Server) loadPointsPanel(r *http.Request, f *personForm) error {
	ctx := r.Context()
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, hhOf(r))
	sum, err := s.Chores.PersonSummary(ctx, hhOf(r), f.ID, loc.StartOfWeek(s.now()))
	if err != nil {
		return err
	}
	f.Points.Points, f.Points.HeldPoints = sum.Points, sum.HeldPoints
	f.Points.Pence, f.Points.HeldPence = sum.Pence, sum.HeldPence
	if f.Points.Suggestions, err = s.Chores.ReasonSuggestions(ctx, hhOf(r)); err != nil {
		return err
	}
	f.Points.History, err = s.Chores.Adjustments(ctx, hhOf(r), f.ID, s.now().Add(-adjustmentHistory), 20)
	return err
}

// handleAdjustPoints gives someone a bonus or takes points away, from the
// Points section of their admin page.
func (s *Server) handleAdjustPoints(w http.ResponseWriter, r *http.Request) {
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
	f := newForm(r)
	in := pointsPanel{
		Direction: f.str("direction"), Amount: f.str("amount"),
		Custom: f.str("amount_custom"), Reason: f.str("reason"),
	}
	if in.Direction != "give" {
		in.Direction = "take"
	}
	fromList := f.str("from") == fromPeople
	again := func(status int, errs valid.Errors, flash string) {
		if fromList {
			s.renderPeople(w, r, status, peopleView{Adjust: &pointsDialog{Person: p, Panel: in}}, errs, flash)
			return
		}
		pf := s.personFormFor(r, p)
		pf.Points = in
		s.renderPerson(w, r, status, pf, errs, flash)
	}
	// done shows a flash without the form: on the list, or on their page.
	done := func(status int, flash string, undo int64) {
		if fromList {
			v := peopleView{}
			if undo != 0 {
				v.Undo = &peopleUndo{PersonID: p.ID, EntryID: undo}
			}
			s.renderPeople(w, r, status, v, nil, flash)
			return
		}
		pf := s.personFormFor(r, p)
		pf.Points.Direction = in.Direction // keep their choice for the next one
		pf.Points.Undo = undo
		s.renderPerson(w, r, status, pf, nil, flash)
	}

	amount := in.Amount
	if amount == "custom" {
		amount = in.Custom
	} else if !slices.Contains(quickAmounts, amount) {
		amount = ""
	}
	n, err := strconv.ParseInt(amount, 10, 64)
	if err != nil || n < 1 {
		n = 0 // AdjustPoints says what's wrong
	}
	signed := n
	if in.Direction == "take" {
		signed = -n
	}
	entry, err := s.Chores.AdjustPoints(r.Context(), hhOf(r), adult.ID, p.ID, signed, in.Reason)
	if errs, ok := validationErrors(err); ok {
		again(http.StatusUnprocessableEntity, errs, "")
		return
	}
	if errors.Is(err, chores.ErrNotAdult) {
		again(http.StatusForbidden, nil, "Only adults can change points.")
		return
	}
	if errors.Is(err, chores.ErrNotFound) { // archived
		done(http.StatusConflict, "Restore them before changing their points.", 0)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	flash := fmt.Sprintf("Gave %s to %s.", pointsWord(n), p.Name)
	if in.Direction == "take" {
		flash = fmt.Sprintf("Took %s from %s.", pointsWord(n), p.Name)
	}
	done(http.StatusOK, flash, entry.ID)
}

// handleUndoAdjustment removes an adjustment made in the last few minutes.
func (s *Server) handleUndoAdjustment(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	entryID, err := strconv.ParseInt(r.PathValue("entry"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	reply := func(status int, flash string) {
		if r.PostFormValue("from") == fromPeople {
			s.renderPeople(w, r, status, peopleView{}, nil, flash)
			return
		}
		s.renderPerson(w, r, status, s.personFormFor(r, p), nil, flash)
	}
	err = s.Chores.UndoAdjustment(r.Context(), hhOf(r), adult.ID, entryID)
	switch {
	case errors.Is(err, chores.ErrUndoExpired):
		reply(http.StatusConflict, "That can't be undone any more.")
		return
	case errors.Is(err, chores.ErrNotAdult):
		reply(http.StatusForbidden, "Only adults can change points.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	reply(http.StatusOK, "Undone.")
}

// pointsWord is "1 point" or "5 points".
func pointsWord(n int64) string {
	if n == 1 {
		return "1 point"
	}
	return strconv.FormatInt(n, 10) + " points"
}
