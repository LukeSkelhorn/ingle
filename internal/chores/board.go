package chores

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// OverdueDays is how far back the board looks for missed chores.
const OverdueDays = 14

// UndoWindow is how long after completing a chore it can be undone from the
// board without an adult. Later corrections are an admin job.
const UndoWindow = 30 * time.Second

// ErrUndoExpired is returned by Undo once UndoWindow has passed or an adult
// has already reviewed the completion.
var ErrUndoExpired = errors.New("too late to undo")

// Board is everything the main board shows for one board day.
type Board struct {
	Day     string // YYYY-MM-DD
	Pool    []Card
	Columns []Column
	People  []dbq.Person // active people, for the "who's doing this?" picker
}

// Column is one person's chores and balance (what they can spend: waiting
// reward claims are held back, and Waiting counts them). TotalToday counts every card
// in the column (today's chores plus any overdue ones), so "all done" means
// nothing is left, not just today's.
type Column struct {
	Person     dbq.Person
	Points     int64
	Pence      int64
	Waiting    int
	DoneToday  int
	TotalToday int
	Cards      []Card
}

// Card is one occurrence on the board.
type Card struct {
	OccurrenceID int64
	Title        string
	DueOn        string
	DueTime      *string
	Points       int64
	Pence        int64
	Pool         bool  // a pool chore (claimable, or claimed by this column's person)
	Claimed      bool  // a pool chore claimed by this column's person
	Overdue      bool  // due before today and not done
	Done         bool  // completed (pending or approved)
	Pending      bool  // completed, waiting for an adult
	CompletionID int64 // set when Done
	CompletedAt  int64 // unix seconds, set when Done
	JustDone     bool  // completed in the last few seconds (for the tick animation)
}

// justDoneFor is how long a completed card counts as JustDone.
const justDoneFor = 10 * time.Second

// Board builds the board for boardDay (the local midnight from
// settings.BoardDay). It shows today's occurrences plus overdue ones: an
// undone occurrence from the last OverdueDays days that is still the latest
// occurrence of its chore, so a missed weekly chore stays up until the next
// one is due but a missed daily chore doesn't pile up. An overdue chore
// finished today stays up (ticked) until the day rolls over.
func (s *Service) Board(ctx context.Context, householdID int64, boardDay time.Time) (Board, error) {
	today := boardDay.Format(DateLayout)
	from := boardDay.AddDate(0, 0, -OverdueDays).Format(DateLayout)
	q := s.st.Q

	people, err := q.ListPeople(ctx, householdID)
	if err != nil {
		return Board{}, fmt.Errorf("list people: %w", err)
	}
	balances, err := q.BalancesForHousehold(ctx, householdID)
	if err != nil {
		return Board{}, fmt.Errorf("balances: %w", err)
	}
	held, err := q.HeldForHousehold(ctx, householdID)
	if err != nil {
		return Board{}, fmt.Errorf("held: %w", err)
	}
	rows, err := q.ListBoard(ctx, dbq.ListBoardParams{HouseholdID: householdID, FromDate: from, ToDate: today})
	if err != nil {
		return Board{}, fmt.Errorf("list board: %w", err)
	}
	comps, err := q.ListLiveCompletionsForRange(ctx, dbq.ListLiveCompletionsForRangeParams{
		HouseholdID: householdID, FromDate: from, ToDate: today,
	})
	if err != nil {
		return Board{}, fmt.Errorf("list completions: %w", err)
	}

	byOcc := make(map[int64]dbq.ListLiveCompletionsForRangeRow, len(comps))
	for _, c := range comps {
		byOcc[c.OccurrenceID] = c
	}
	latest := map[int64]string{} // chore → latest due_on up to today
	for _, r := range rows {
		if r.DueOn > latest[r.ChoreID] {
			latest[r.ChoreID] = r.DueOn
		}
	}

	b := Board{Day: today, People: people}
	cols := make(map[int64]*Column, len(people))
	b.Columns = make([]Column, len(people))
	for i, p := range people {
		b.Columns[i] = Column{Person: p}
		cols[p.ID] = &b.Columns[i]
	}
	for _, bal := range balances {
		if c := cols[bal.PersonID]; c != nil {
			c.Points, c.Pence = bal.Points, bal.Pence
		}
	}
	for _, h := range held {
		if c := cols[h.PersonID]; c != nil {
			c.Points -= h.Points
			c.Pence -= h.Pence
			c.Waiting = int(h.Waiting)
		}
	}

	for _, r := range rows {
		if r.Status == "skipped" {
			continue
		}
		comp, done := byOcc[r.ID]
		if r.DueOn < today {
			if latest[r.ChoreID] != r.DueOn {
				continue // superseded by a newer occurrence
			}
			if done && comp.CompletedAt < boardDay.Unix() {
				continue // caught up on an earlier day
			}
		}
		card := Card{
			OccurrenceID: r.ID,
			Title:        r.Title,
			DueOn:        r.DueOn,
			DueTime:      r.DueTime,
			Points:       r.Points,
			Pence:        r.Pence,
			Pool:         r.Mode == ModePool,
			Overdue:      r.DueOn < today && !done,
			Done:         done,
		}
		if done {
			card.Pending = comp.Status == StatusPending
			card.CompletionID = comp.ID
			card.CompletedAt = comp.CompletedAt
			card.JustDone = s.now().Sub(time.Unix(comp.CompletedAt, 0)) < justDoneFor
		}

		owner := r.AssigneeID
		if owner == nil {
			owner = r.ClaimedBy
		}
		if owner == nil && done {
			// An unclaimed pool chore someone just did: show it under them.
			owner = &comp.PersonID
		}
		if owner == nil {
			b.Pool = append(b.Pool, card)
			continue
		}
		col := cols[*owner]
		if col == nil {
			continue // owner archived since
		}
		card.Claimed = card.Pool && !done
		col.Cards = append(col.Cards, card)
		col.TotalToday++
		if done {
			col.DoneToday++
		}
	}

	slices.SortStableFunc(b.Pool, compareCards)
	for i := range b.Columns {
		slices.SortStableFunc(b.Columns[i].Cards, compareCards)
	}
	return b, nil
}

// compareCards orders a column: overdue, then due today (timed before
// untimed, by time), then done; ties by title.
func compareCards(a, b Card) int {
	rank := func(c Card) int {
		switch {
		case c.Done:
			return 2
		case c.Overdue:
			return 0
		default:
			return 1
		}
	}
	if d := cmp.Compare(rank(a), rank(b)); d != 0 {
		return d
	}
	if d := cmp.Compare(a.DueOn, b.DueOn); d != 0 {
		return d
	}
	switch {
	case a.DueTime != nil && b.DueTime == nil:
		return -1
	case a.DueTime == nil && b.DueTime != nil:
		return 1
	case a.DueTime != nil && b.DueTime != nil:
		if d := cmp.Compare(*a.DueTime, *b.DueTime); d != 0 {
			return d
		}
	}
	return cmp.Compare(a.Title, b.Title)
}

// Undo reverses a board tap: within UndoWindow of completing, and only if no
// adult has reviewed it, the completion and its ledger credit are removed and
// the occurrence reopens (claimed again if it was claimed). The activity row
// is attributed to whoever completed it.
func (s *Service) Undo(ctx context.Context, householdID, completionID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		c, err := q.GetCompletion(ctx, dbq.GetCompletionParams{HouseholdID: householdID, ID: completionID})
		if err != nil {
			return notFound(err)
		}
		if c.Status == StatusRejected || c.ReviewedBy != nil ||
			s.now().Sub(time.Unix(c.CompletedAt, 0)) > UndoWindow {
			return ErrUndoExpired
		}
		if err := q.DeleteLedgerForCompletion(ctx, dbq.DeleteLedgerForCompletionParams{
			HouseholdID: householdID, CompletionID: &c.ID,
		}); err != nil {
			return fmt.Errorf("delete ledger: %w", err)
		}
		if err := q.DeleteCompletion(ctx, dbq.DeleteCompletionParams{HouseholdID: householdID, ID: c.ID}); err != nil {
			return fmt.Errorf("delete completion: %w", err)
		}
		if err := q.ReopenOccurrence(ctx, dbq.ReopenOccurrenceParams{HouseholdID: householdID, ID: c.OccurrenceID}); err != nil {
			return fmt.Errorf("reopen occurrence: %w", err)
		}
		return s.activity(ctx, q, householdID, c.PersonID, "chore.undone", c.OccurrenceID)
	})
}
