package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/lukeskelhorn/domus/internal/chores"
)

// completionsPage is Admin › Completions: chores and reward claims waiting
// for a grown-up, and the last week of completions that can still be
// reversed.
type completionsPage struct {
	Pending []chores.CompletionDetail
	Claims  []chores.RedemptionDetail
	Missed  []chores.MissedDetail
	Recent  []chores.CompletionDetail
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	s.renderCompletions(w, r, http.StatusOK, "")
}

func (s *Server) renderCompletions(w http.ResponseWriter, r *http.Request, status int, flash string) {
	all, err := s.Chores.Completions(r.Context(), s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	claims, err := s.Chores.PendingRedemptions(r.Context(), s.HouseholdID, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	missed, err := s.Chores.Missed(r.Context(), s.HouseholdID, s.boardDay(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := completionsPage{Claims: claims, Missed: missed}
	for _, c := range all {
		if c.Pending() {
			p.Pending = append(p.Pending, c)
		} else {
			p.Recent = append(p.Recent, c)
		}
	}
	v := s.view(r, "Completions", p)
	v.Nav = "completions"
	v.Flash = flash
	s.render(w, r, status, "admin_completions", v)
}

// adminReview runs approve, reject or reverse (of a completion, or a reward
// claim) as the signed-in adult.
func (s *Server) adminReview(fn func(ctx context.Context, hh, id, reviewerID int64) error, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		adult := boardAdult(r)
		if adult == nil { // requireAdmin guarantees a session; belt and braces
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := fn(r.Context(), s.HouseholdID, id, adult.ID); err != nil {
			text, known := boardErrorText(err)
			if !known {
				s.serverError(w, r, err)
				return
			}
			s.renderCompletions(w, r, http.StatusConflict, text)
			return
		}
		s.renderCompletions(w, r, http.StatusOK, msg)
	}
}

// handleMarkDone marks a missed Only on the day chore done, as the person
// chosen (or whose it was), approved by the signed-in adult.
func (s *Server) handleMarkDone(w http.ResponseWriter, r *http.Request) {
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
	person, err := strconv.ParseInt(r.PostFormValue("person"), 10, 64)
	if err != nil || person <= 0 {
		s.renderCompletions(w, r, http.StatusBadRequest, "Choose who did it.")
		return
	}
	if err := s.Chores.MarkDone(r.Context(), s.HouseholdID, id, person, adult.ID); err != nil {
		text, known := boardErrorText(err)
		if !known {
			s.serverError(w, r, err)
			return
		}
		s.renderCompletions(w, r, http.StatusConflict, text)
		return
	}
	s.renderCompletions(w, r, http.StatusOK, "Marked done. Points added.")
}
