package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type boardView struct {
	chores.Board
	Adult bool
	// Relative seconds, so a display with a wrong clock still refreshes on time.
	NextIn     int64
	OOB        bool
	Countdowns []chores.Countdown
}

// PoolLate reports whether any chore up for grabs is overdue.
func (v boardView) PoolLate() bool {
	for _, c := range v.Pool {
		if c.Overdue {
			return true
		}
	}
	return false
}

type boardPage struct {
	Clock clockView
	Board boardView
	Lock  lockView
}

type pickerView struct {
	Card   chores.Card
	People []pickerPerson
}

type pickerPerson struct {
	Person        dbq.Person
	Points, Pence int64
}

func newPickerView(c chores.Card, people []dbq.Person) pickerView {
	v := pickerView{Card: c, People: make([]pickerPerson, len(people))}
	for i, p := range people {
		v.People[i].Person = p
		v.People[i].Points, v.People[i].Pence = chores.Earns(p.Kind, c.Points, c.Pence, c.AdultPoints)
	}
	return v
}

func (s *Server) loadBoard(r *http.Request) (boardView, error) {
	b, err := s.Chores.Board(r.Context(), hhOf(r), s.boardDay(r))
	if err != nil {
		return boardView{}, err
	}
	v := boardView{Board: b, Adult: identityFrom(r.Context()).Session != nil}
	cs, err := s.countdowns(r)
	if err != nil {
		return boardView{}, err
	}
	v.Countdowns = cs[:min(len(cs), stripCountdowns)]
	if !b.NextChange.IsZero() {
		v.NextIn = max(int64(b.NextChange.Sub(s.now())/time.Second)+1, 1)
	}
	return v, nil
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

func (s *Server) renderBoard(w http.ResponseWriter, r *http.Request, status int) {
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	b.OOB = true
	s.renderPartialStatus(w, r, status, "board", b)
}

func (s *Server) handlePoolSheet(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBoard(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "pool_sheet", b)
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
			s.renderPartial(w, r, "picker", newPickerView(c, b.People))
			return
		}
	}
	setToast(w, toast{Message: "Someone's already on that one.", Error: true})
	s.renderPartialStatus(w, r, http.StatusConflict, "pool_sheet", b)
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
	if err := s.Chores.Claim(r.Context(), hhOf(r), id, *person); err != nil {
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
		occ, err := s.Store.Q.GetOccurrence(r.Context(), dbq.GetOccurrenceParams{HouseholdID: hhOf(r), ID: id})
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
	c, err := s.Chores.Complete(r.Context(), hhOf(r), id, *person)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	t := toast{
		Message: "Nice one, " + s.personName(r, *person) + "!",
		Undo:    "/board/completions/" + itoa(c.ID) + "/undo",
	}
	if s.Chores.IsShared(r.Context(), hhOf(r), id) {
		t.Message += " Done for everyone."
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
	occ, err := s.Store.Q.GetOccurrence(r.Context(), dbq.GetOccurrenceParams{HouseholdID: hhOf(r), ID: id})
	if err != nil || occ.ClaimedBy == nil {
		s.boardFailed(w, r, "That's not claimed any more.")
		return
	}
	if err := s.Chores.Release(r.Context(), hhOf(r), id, *occ.ClaimedBy); err != nil {
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
	if err := s.Chores.Undo(r.Context(), hhOf(r), id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Undone."})
}

type reviewView struct {
	C chores.CompletionDetail
}

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
	c, err := s.Chores.Completion(r.Context(), hhOf(r), id)
	if err != nil {
		s.boardError(w, r, err)
		return
	}
	s.renderPartial(w, r, "review", reviewView{C: c})
}

const needAdult = "Ask a grown-up to unlock admin."

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
		if err := fn(r.Context(), hhOf(r), id, adult.ID); err != nil {
			s.boardError(w, r, err)
			return
		}
		s.boardDone(w, r, toast{Message: msg})
	}
}

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

// asciiJSON escapes non-ASCII as \uXXXX because browsers read header values as Latin-1.
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

func (s *Server) boardDone(w http.ResponseWriter, r *http.Request, t toast) {
	s.events.publish(hhOf(r), boardClient(r.Header.Get(boardClientHeader)))
	setToast(w, t)
	s.renderBoard(w, r, http.StatusOK)
}

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
	case errors.Is(err, chores.ErrTooLate):
		return "Too late for that one.", true
	case errors.Is(err, chores.ErrNotMissed):
		return "That one's not on the missed list any more.", true
	case errors.Is(err, chores.ErrNotYours):
		return "That's someone else's chore.", true
	case errors.Is(err, chores.ErrNotClaimable):
		return "That chore isn't up for grabs.", true
	case errors.Is(err, chores.ErrNotClaimedByYou):
		return "That's not claimed any more.", true
	case errors.Is(err, chores.ErrTransferPending):
		return "You've already asked. Wait for a grown-up.", true
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
	case errors.Is(err, chores.ErrNotChild):
		return "Only a child's chore can be done for them.", true
	case errors.Is(err, chores.ErrInsufficientBalance):
		return "Not enough for that yet.", true
	case errors.Is(err, chores.ErrNotFound):
		return "That's gone.", true
	}
	return "", false
}

func (s *Server) personName(r *http.Request, id int64) string {
	p, err := s.Store.Q.GetPerson(r.Context(), dbq.GetPersonParams{HouseholdID: hhOf(r), ID: id})
	if err != nil {
		return "you"
	}
	return p.Name
}

type personSheetView struct {
	chores.Summary
	Waiting     []chores.RedemptionDetail
	Rewards     []rewardRow
	Adjustments []chores.Adjustment
	Adult       bool
	MoneyHere   bool
}

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
	loc.Location = s.Settings.Location(ctx, hhOf(r))
	sum, err := s.Chores.PersonSummary(ctx, hhOf(r), id, loc.StartOfWeek(s.now()))
	if errors.Is(err, chores.ErrNotFound) {
		s.boardFailed(w, r, "They're not on the board any more.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rewards, err := s.Household.RewardsFor(ctx, hhOf(r), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	waiting, err := s.Chores.PendingRedemptions(ctx, hhOf(r), &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	adjustments, err := s.Chores.Adjustments(ctx, hhOf(r), id, s.now().Add(-adjustmentHistory), 5)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := personSheetView{
		Summary: sum, Waiting: waiting, Rewards: make([]rewardRow, 0, len(rewards)),
		Adjustments: adjustments, Adult: boardAdult(r) != nil,
		MoneyHere: identityFrom(ctx).Device != nil || moneyAdult(r),
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
	red, err := s.Chores.ClaimReward(r.Context(), hhOf(r), id, *person, approver)
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
	rw, err := s.Chores.SetGoal(r.Context(), hhOf(r), id, *rewardID)
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

func (s *Server) handleGoalClear(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if err := s.Chores.ClearGoal(r.Context(), hhOf(r), id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Goal removed"})
}

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
	d, err := s.Chores.Redemption(r.Context(), hhOf(r), id)
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

func (s *Server) handleClaimCancel(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.boardAction(w, r)
	if !ok {
		return
	}
	if err := s.Chores.CancelRedemption(r.Context(), hhOf(r), id); err != nil {
		s.boardError(w, r, err)
		return
	}
	s.boardDone(w, r, toast{Message: "Cancelled. Points are free again."})
}
