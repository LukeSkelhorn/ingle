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
	loc.Location = s.Settings.Location(ctx, s.HouseholdID)
	sum, err := s.Chores.PersonSummary(ctx, s.HouseholdID, f.ID, loc.StartOfWeek(s.now()))
	if err != nil {
		return err
	}
	f.Points.Points, f.Points.HeldPoints = sum.Points, sum.HeldPoints
	if f.Points.Suggestions, err = s.Chores.ReasonSuggestions(ctx, s.HouseholdID); err != nil {
		return err
	}
	f.Points.History, err = s.Chores.Adjustments(ctx, s.HouseholdID, f.ID, s.now().Add(-adjustmentHistory), 20)
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
	again := func(status int, errs valid.Errors, flash string) {
		pf := s.personFormFor(r, p)
		pf.Points = in
		s.renderPerson(w, r, status, pf, errs, flash)
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
	entry, err := s.Chores.AdjustPoints(r.Context(), s.HouseholdID, adult.ID, p.ID, signed, in.Reason)
	if errs, ok := validationErrors(err); ok {
		again(http.StatusUnprocessableEntity, errs, "")
		return
	}
	if errors.Is(err, chores.ErrNotAdult) {
		again(http.StatusForbidden, nil, "Only adults can change points.")
		return
	}
	if errors.Is(err, chores.ErrNotFound) { // archived
		s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, p), nil, "Restore them before changing their points.")
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
	pf := s.personFormFor(r, p)
	pf.Points.Direction = in.Direction // keep their choice for the next one
	pf.Points.Undo = entry.ID
	s.renderPerson(w, r, http.StatusOK, pf, nil, flash)
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
	err = s.Chores.UndoAdjustment(r.Context(), s.HouseholdID, adult.ID, entryID)
	switch {
	case errors.Is(err, chores.ErrUndoExpired):
		s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, p), nil, "That can't be undone any more.")
		return
	case errors.Is(err, chores.ErrNotAdult):
		s.renderPerson(w, r, http.StatusForbidden, s.personFormFor(r, p), nil, "Only adults can change points.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil, "Undone.")
}

// pointsWord is "1 point" or "5 points".
func pointsWord(n int64) string {
	if n == 1 {
		return "1 point"
	}
	return strconv.FormatInt(n, 10) + " points"
}
