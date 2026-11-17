package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lukeskelhorn/ingle/internal/chores"
)

type completionsPage struct {
	Pending []chores.CompletionDetail
	Claims  []chores.RedemptionDetail
	Missed  []chores.MissedDetail
	Recent  []chores.CompletionDetail
	Undo    []int64
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	s.renderCompletions(w, r, http.StatusOK, "")
}

func (s *Server) renderCompletions(w http.ResponseWriter, r *http.Request, status int, flash string) {
	s.renderCompletionsUndo(w, r, status, flash, nil)
}

func (s *Server) renderCompletionsUndo(w http.ResponseWriter, r *http.Request, status int, flash string, undo []int64) {
	all, err := s.Chores.Completions(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	claims, err := s.Chores.PendingRedemptions(r.Context(), hhOf(r), nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	missed, err := s.Chores.Missed(r.Context(), hhOf(r), s.boardDay(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !s.moduleOn(r.Context(), modRewards) {
		claims = nil
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

func (s *Server) adminReview(fn func(ctx context.Context, hh, id, reviewerID int64) error, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if err := fn(r.Context(), hhOf(r), id, adult.ID); err != nil {
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
	if err := s.Chores.MarkDone(r.Context(), hhOf(r), id, person, adult.ID); err != nil {
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
	done, err := s.Chores.Dismiss(r.Context(), hhOf(r), adult.ID, ids)
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
	if _, err := s.Chores.Restore(r.Context(), hhOf(r), adult.ID, ids); err != nil {
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
