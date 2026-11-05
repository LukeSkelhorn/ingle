package server

import (
	"context"
	"net/http"

	"github.com/lukeskelhorn/domus/internal/chores"
)

// completionsPage is Admin › Completions: chores and reward claims waiting
// for a grown-up, and the last week of completions that can still be
// reversed.
type completionsPage struct {
	Pending []chores.CompletionDetail
	Claims  []chores.RedemptionDetail
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
	p := completionsPage{Claims: claims}
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
