package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// boardView is what the "board" partial renders.
type boardView struct {
	chores.Board
	// Adult is set when an adult is signed in, for the empty-state admin link.
	Adult bool
}

// boardPage is the full board page: clock plus the board.
type boardPage struct {
	Clock clockView
	Board boardView
}

// pickerView is the "who's doing this?" sheet for a pool chore.
type pickerView struct {
	Card   chores.Card
	People []dbq.Person
}

func (s *Server) loadBoard(r *http.Request) (boardView, error) {
	b, err := s.Chores.Board(r.Context(), s.HouseholdID, s.boardDay(r))
	if err != nil {
		return boardView{}, err
	}
	return boardView{Board: b, Adult: identityFrom(r.Context()).Session != nil}, nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "board", s.view(r, "", boardPage{Clock: s.clock(r.Context()), Board: b}))
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	s.renderBoard(w, r, http.StatusOK)
}

// renderBoard re-renders the #board fragment, the response to every board
// action so the tapping screen updates without waiting for its own event.
func (s *Server) renderBoard(w http.ResponseWriter, r *http.Request, status int) {
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartialStatus(w, r, status, "board", b)
}

func (s *Server) handlePick(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, c := range b.Pool {
		if c.OccurrenceID == id {
			s.renderPartial(w, r, "picker", pickerView{Card: c, People: b.People})
			return
		}
	}
	// Claimed or done elsewhere since this screen last refreshed.
	s.boardFailed(w, r, "Someone's already on that one.")
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	id, person, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if person == nil {
		s.boardFailed(w, r, "Choose who's doing it.")
		return
	}
	if err := s.Chores.Claim(r.Context(), s.HouseholdID, id, *person); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: s.personName(r, *person) + " is on it."})
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	id, person, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if person == nil {
		// Assigned and claimed chores already know whose they are.
		occ, err := s.Store.Q.GetOccurrence(r.Context(), dbq.GetOccurrenceParams{HouseholdID: s.HouseholdID, ID: id})
		if err != nil {
			s.boardError(w, r, chores.ErrNotFound)
			return
		}
		person = occ.AssigneeID
		if person == nil {
			person = occ.ClaimedBy
		}
		if person == nil {
			s.boardFailed(w, r, "Choose who did it.")
			return
		}
	}
	c, err := s.Chores.Complete(r.Context(), s.HouseholdID, id, *person)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	t := toast{
		Message: "Nice one, " + s.personName(r, *person) + "!",
		Undo:    "/board/completions/" + itoa(c.ID) + "/undo",
	}
	if c.Status == chores.StatusPending {
		t.Message = "Done! A grown-up will check it."
	}
	s.boardDone(w, r, t)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	occ, err := s.Store.Q.GetOccurrence(r.Context(), dbq.GetOccurrenceParams{HouseholdID: s.HouseholdID, ID: id})
	if err != nil || occ.ClaimedBy == nil {
		s.boardFailed(w, r, "That's not claimed any more.")
		return
	}
	if err := s.Chores.Release(r.Context(), s.HouseholdID, id, *occ.ClaimedBy); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Back up for grabs."})
}

func (s *Server) handleUndo(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if err := s.Chores.Undo(r.Context(), s.HouseholdID, id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Undone."})
}

// boardAction parses a board POST: the {id} path value and optional person.
func (s *Server) boardAction(w http.ResponseWriter, r *http.Request) (int64, *int64, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return 0, nil, false
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return 0, nil, false
	}
	return id, newForm(r).optID("person"), true
}

// toast is a short message shown at the bottom of the board, optionally with
// an Undo button that POSTs to Undo.
type toast struct {
	Message string `json:"message"`
	Undo    string `json:"undo,omitempty"`
	Error   bool   `json:"error,omitempty"`
}

func setToast(w http.ResponseWriter, t toast) {
	b, err := json.Marshal(map[string]toast{"toast": t})
	if err != nil {
		return
	}
	w.Header().Set("HX-Trigger", string(b))
}

// boardDone finishes a successful board action: tell every other screen and
// return the fresh board with a toast.
func (s *Server) boardDone(w http.ResponseWriter, r *http.Request, t toast) {
	s.events.publish(s.HouseholdID, boardClient(r.Header.Get(boardClientHeader)))
	setToast(w, t)
	s.renderBoard(w, r, http.StatusOK)
}

// boardFailed returns the current board with an error toast, so a stale
// screen corrects itself.
func (s *Server) boardFailed(w http.ResponseWriter, r *http.Request, msg string) {
	setToast(w, toast{Message: msg, Error: true})
	s.renderBoard(w, r, http.StatusConflict)
}

func (s *Server) boardError(w http.ResponseWriter, r *http.Request, err error) {
	if msg, ok := boardErrorText(err); ok {
		s.boardFailed(w, r, msg)
		return
	}
	s.serverError(w, r, err)
}

func boardErrorText(err error) (string, bool) {
	switch {
	case errors.Is(err, chores.ErrAlreadyClaimed):
		return "Someone's already on that one.", true
	case errors.Is(err, chores.ErrNotOpen):
		return "That's already done.", true
	case errors.Is(err, chores.ErrNotYours):
		return "That's someone else's chore.", true
	case errors.Is(err, chores.ErrNotClaimable):
		return "That chore isn't up for grabs.", true
	case errors.Is(err, chores.ErrNotClaimedByYou):
		return "That's not claimed any more.", true
	case errors.Is(err, chores.ErrUndoExpired):
		return "Too late to undo. Ask a grown-up.", true
	case errors.Is(err, chores.ErrNotFound):
		return "That chore has gone.", true
	}
	return "", false
}

func (s *Server) personName(r *http.Request, id int64) string {
	p, err := s.Store.Q.GetPerson(r.Context(), dbq.GetPersonParams{HouseholdID: s.HouseholdID, ID: id})
	if err != nil {
		return "you"
	}
	return p.Name
}
