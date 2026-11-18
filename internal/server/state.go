package server

import (
	"context"
	"log/slog"
	"net/http"
)

// HouseholdState is how a household is, as the program running Ingle says.
type HouseholdState struct {
	ReadOnly                        bool
	Notice, NoticeLabel, NoticePath string
}

const readOnlyText = "Changes are turned off for now."

// state fails open: an error leaves the household changeable.
func (s *Server) state(ctx context.Context, hh int64) HouseholdState {
	if s.State == nil || hh == 0 {
		return HouseholdState{}
	}
	st, err := s.State(ctx, hh)
	if err != nil {
		s.Log.ErrorContext(ctx, "household state", slog.Int64("household", hh), slog.Any("err", err))
		return HouseholdState{}
	}
	return st
}

// writable refuses changes while the household is read-only: a toast on the board, a page elsewhere.
func (s *Server) writable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		st := s.state(r.Context(), hhOf(r))
		if !st.ReadOnly {
			next.ServeHTTP(w, r)
			return
		}
		msg := st.Notice
		if msg == "" {
			msg = readOnlyText
		}
		if r.Header.Get("HX-Request") == "true" && r.Header.Get(boardClientHeader) != "" {
			s.boardFailed(w, r, msg)
			return
		}
		s.render(w, r, http.StatusForbidden, "readonly", s.view(r, "Can't change that", msg))
	})
}
