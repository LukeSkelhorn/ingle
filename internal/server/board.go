package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// boardView is what the "board" partial renders.
type boardView struct {
	chores.Board
	// Adult is set when an adult is signed in (or unlocked on a display): it
	// shows the admin link on an empty board and makes done chores tappable
	// for review.
	Adult bool
}

// boardPage is the full board page: clock plus the board.
type boardPage struct {
	Clock clockView
	Board boardView
	Lock  lockView
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
	lv, err := s.boardLock(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if lv.Locked && identityFrom(r.Context()).Device != nil {
		s.renderLocked(w, r)
		return
	}
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "board", s.view(r, "", boardPage{Clock: s.clock(r.Context()), Board: b, Lock: lv}))
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

// reviewView is the adult's sheet for a done chore.
type reviewView struct {
	C chores.CompletionDetail
}

// boardAdult is the adult on this request's session, if any.
func boardAdult(r *http.Request) *dbq.Person {
	if sess := identityFrom(r.Context()).Session; sess != nil {
		return &sess.Person
	}
	return nil
}

func (s *Server) handleReviewSheet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if boardAdult(r) == nil {
		s.boardFailed(w, r, needAdult)
		return
	}
	c, err := s.Chores.Completion(r.Context(), s.HouseholdID, id)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	s.renderPartial(w, r, "review", reviewView{C: c})
}

const needAdult = "Ask a grown-up to unlock admin."

// boardReview runs an adult action (approve, reject, reverse) from the board.
func (s *Server) boardReview(fn func(ctx context.Context, hh, id, reviewerID int64) error, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _, ok := s.boardAction(w, r)
		if !ok {
			return
		}
		adult := boardAdult(r)
		if adult == nil {
			setToast(w, toast{Message: needAdult, Error: true})
			s.renderBoard(w, r, http.StatusForbidden)
			return
		}
		if err := fn(r.Context(), s.HouseholdID, id, adult.ID); err != nil {
			s.boardError(w, r, err)
			return
		}
		s.boardDone(w, r, toast{Message: msg})
	}
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
	w.Header().Set("HX-Trigger", asciiJSON(b))
}

// asciiJSON escapes non-ASCII characters in JSON as \uXXXX. Browsers read
// header values as Latin-1, so "Zoë" or an emoji sent as UTF-8 in a header
// comes out garbled. Non-ASCII can only be inside JSON strings, so escaping
// it there is safe.
func asciiJSON(b []byte) string {
	var sb strings.Builder
	for _, r := range string(b) {
		if r < utf8.RuneSelf {
			sb.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&sb, `\u%04x`, u)
		}
	}
	return sb.String()
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
	case errors.Is(err, chores.ErrTooOld):
		return "That was over a week ago, so it can't be reversed.", true
	case errors.Is(err, chores.ErrNotCounted):
		return "That's already not counted.", true
	case errors.Is(err, chores.ErrNotPending):
		return "Someone has already checked that one.", true
	case errors.Is(err, chores.ErrNotAdult):
		return needAdult, true
	case errors.Is(err, chores.ErrInsufficientBalance):
		return "Not enough for that yet.", true
	case errors.Is(err, chores.ErrNotFound):
		return "That's gone.", true
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

// personSheetView is a person's sheet: balance, this week, their waiting
// reward claims and the rewards. Adult makes waiting claims tappable for
// review.
type personSheetView struct {
	chores.Summary
	Waiting []chores.RedemptionDetail
	Rewards []rewardRow
	Adult   bool
}

// rewardRow is one reward on a person's sheet. NeedPoints and NeedPence are
// how much more they need; a reward is affordable when both are zero.
type rewardRow struct {
	dbq.Reward
	NeedPoints, NeedPence int64
}

func (r rewardRow) Affordable() bool { return r.NeedPoints == 0 && r.NeedPence == 0 }

func (s *Server) handlePersonSheet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, s.HouseholdID)
	sum, err := s.Chores.PersonSummary(ctx, s.HouseholdID, id, loc.StartOfWeek(s.now()))
	if errors.Is(err, chores.ErrNotFound) {
		// Archived since this screen last refreshed.
		s.boardFailed(w, r, "They're not on the board any more.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rewards, err := s.Household.Rewards(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	waiting, err := s.Chores.PendingRedemptions(ctx, s.HouseholdID, &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := personSheetView{
		Summary: sum, Waiting: waiting, Rewards: make([]rewardRow, 0, len(rewards)),
		Adult: boardAdult(r) != nil,
	}
	for _, rw := range rewards {
		v.Rewards = append(v.Rewards, rewardRow{
			Reward:     rw,
			NeedPoints: max(0, rw.CostPoints-sum.Points),
			NeedPence:  max(0, rw.CostPence-sum.Pence),
		})
	}
	s.renderPartial(w, r, "person", v)
}

// handleRewardClaim is a child asking for a reward from their sheet. With an
// adult there (signed in or unlocked) it's approved at once.
func (s *Server) handleRewardClaim(w http.ResponseWriter, r *http.Request) {
	id, person, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if person == nil {
		http.Error(w, "who is claiming?", http.StatusBadRequest)
		return
	}
	var approver *int64
	if adult := boardAdult(r); adult != nil {
		approver = &adult.ID
	}
	red, err := s.Chores.ClaimReward(r.Context(), s.HouseholdID, id, *person, approver)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	msg := "Asked a grown-up 🎁"
	if red.Status == chores.StatusApproved {
		msg = "Enjoy, " + s.personName(r, *person) + "! 🎁"
	}
	s.boardDone(w, r, toast{Message: msg})
}

// handleGoalSet has a person save for a reward. Like completing a chore,
// anyone at the board can do it.
func (s *Server) handleGoalSet(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	rewardID := newForm(r).optID("reward")
	if rewardID == nil {
		http.Error(w, "which reward?", http.StatusBadRequest)
		return
	}
	rw, err := s.Chores.SetGoal(r.Context(), s.HouseholdID, id, *rewardID)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	emoji := rw.Emoji
	if emoji == "" {
		emoji = chores.DefaultGoalEmoji
	}
	s.boardDone(w, r, toast{Message: "Saving for " + rw.Title + " " + emoji})
}

// handleGoalClear stops a person saving for anything.
func (s *Server) handleGoalClear(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if err := s.Chores.ClearGoal(r.Context(), s.HouseholdID, id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Goal removed"})
}

// claimReviewView is an adult's sheet for a waiting reward claim.
type claimReviewView struct {
	D chores.RedemptionDetail
}

func (s *Server) handleClaimReviewSheet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if boardAdult(r) == nil {
		s.boardFailed(w, r, needAdult)
		return
	}
	d, err := s.Chores.Redemption(r.Context(), s.HouseholdID, id)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	if !d.Pending() {
		s.boardError(w, r, chores.ErrNotPending)
		return
	}
	s.renderPartial(w, r, "claim_review", claimReviewView{D: d})
}

// handleClaimCancel is a child changing their mind about a waiting claim.
// Like completing a chore, anyone at the board can do it.
func (s *Server) handleClaimCancel(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if err := s.Chores.CancelRedemption(r.Context(), s.HouseholdID, id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Cancelled. Points are free again."})
}
