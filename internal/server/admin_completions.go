package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lukeskelhorn/ingle/internal/chores"
)

// completionsPage is Admin › Completions: chores and reward claims waiting
// for a grown-up, and the last week of completions that can still be
// reversed.
type completionsPage struct {
	Pending []chores.CompletionDetail
	Claims  []chores.RedemptionDetail
	Missed  []chores.MissedDetail
	Recent  []chores.CompletionDetail
	// Undo lists the missed chores just dismissed, for the flash's Undo.
	Undo []int64
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	s.renderCompletions(w, r, http.StatusOK, "")
}

func (s *Server) renderCompletions(w http.ResponseWriter, r *http.Request, status int, flash string) {
	s.renderCompletionsUndo(w, r, status, flash, nil)
}

// renderCompletionsUndo renders Admin › Completions with a flash that can
// undo dismissing the missed chores in undo.
func (s *Server) renderCompletionsUndo(w http.ResponseWriter, r *http.Request, status int, flash string, undo []int64) {
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
	p := completionsPage{Claims: claims, Missed: missed, Undo: undo}
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

// handleDismissMissed takes missed Only on the day chores off the list: the
// one in the path, or (with no id) every id posted, which is what the page
// listed when Dismiss all was pressed.
func (s *Server) handleDismissMissed(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		ids = []int64{id}
	} else {
		var ok bool
		if ids, ok = formIDs(r); !ok {
			s.renderCompletions(w, r, http.StatusBadRequest, "Nothing to dismiss.")
			return
		}
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	done, err := s.Chores.Dismiss(r.Context(), s.HouseholdID, adult.ID, ids)
	if err != nil {
		s.missedError(w, r, err)
		return
	}
	msg := "Dismissed."
	if len(done) > 1 {
		msg = fmt.Sprintf("Dismissed %d missed chores.", len(done))
	}
	s.renderCompletionsUndo(w, r, http.StatusOK, msg, done)
}

// handleRestoreMissed undoes a dismiss, putting the posted ids back on the
// Missed list.
func (s *Server) handleRestoreMissed(w http.ResponseWriter, r *http.Request) {
	ids, ok := formIDs(r)
	if !ok {
		s.renderCompletions(w, r, http.StatusBadRequest, "Nothing to undo.")
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.Chores.Restore(r.Context(), s.HouseholdID, adult.ID, ids); err != nil {
		s.missedError(w, r, err)
		return
	}
	s.renderCompletions(w, r, http.StatusOK, "Back on the missed list.")
}

func (s *Server) missedError(w http.ResponseWriter, r *http.Request, err error) {
	text, known := boardErrorText(err)
	if !known {
		s.serverError(w, r, err)
		return
	}
	s.renderCompletions(w, r, http.StatusConflict, text)
}

// formIDs is the positive "id" values posted, at most 100; false if none.
func formIDs(r *http.Request) ([]int64, bool) {
	if err := r.ParseForm(); err != nil {
		return nil, false
	}
	var ids []int64
	for _, v := range r.PostForm["id"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, len(ids) > 0 && len(ids) <= 100
}
