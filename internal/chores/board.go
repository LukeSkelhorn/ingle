package chores

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lukeskelhorn/domus/internal/emoji"
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
	// NextChange is the next time an Only on the day chore on the board
	// runs out (zero if none), so displays can refresh then.
	NextChange time.Time
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
	Goal       *Goal // what they're saving for, if anything
	DoneToday  int
	TotalToday int
	Cards      []Card
}

// Card is one occurrence on the board.
type Card struct {
	OccurrenceID int64
	Title        string
	Emoji        string // "" for none
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
	// Flexible-day chores, not yet done: Flex names the days left ("Today or
	// Thu"); LastChance marks the last of several days.
	Flex       string
	LastChance bool
	// Shared chores: For is whose column this copy is in (the person a tap
	// completes it as), With the other chosen people. Done shared chores
	// show only under whoever did them.
	Shared bool
	For    int64
	With   []dbq.Person
	SortOn string // the day it sorts with: today, or an overdue due date
	// Only on the day chores, not yet done: they come off the board at their
	// due time (Until, "HH:MM") or, untimed, when the day rolls over.
	OnTheDay bool
	Until    string
}

// FallbackEmoji leads a picture card for a chore with no emoji and nothing
// to suggest from its title.
const FallbackEmoji = "⭐"

// PictureEmoji is the emoji for a picture card, which always leads with one:
// the chore's own, or one suggested from its title, or FallbackEmoji.
func (c Card) PictureEmoji() string {
	if c.Emoji != "" {
		return c.Emoji
	}
	if e := emoji.Suggest(c.Title, emoji.Chore); e != "" {
		return e
	}
	return FallbackEmoji
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
	goals, err := q.ListGoals(ctx, householdID)
	if err != nil {
		return Board{}, fmt.Errorf("goals: %w", err)
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
	// latest is when each chore's newest occurrence on the board came up:
	// its due date, or the first day of a flexible-day window. An overdue
	// occurrence older than that has been superseded.
	latest := map[int64]string{}
	for _, r := range rows {
		if start := availableFrom(r); start > latest[r.ChoreID] {
			latest[r.ChoreID] = start
		}
	}
	loc, dayStart, now := s.loc(ctx, householdID), s.dayStart(ctx, householdID), s.now()
	shared, err := s.sharedPeople(ctx, householdID)
	if err != nil {
		return Board{}, err
	}
	byPerson := make(map[int64]dbq.Person, len(people))
	for _, p := range people {
		byPerson[p.ID] = p
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
	for _, g := range goals {
		if c := cols[g.PersonID]; c != nil {
			goal := newGoal(g, c.Points, c.Pence)
			c.Goal = &goal
		}
	}

	for _, r := range rows {
		if r.Status == "skipped" {
			continue
		}
		comp, done := byOcc[r.ID]
		flex := r.AvailableFrom != nil
		if r.DueOn < today && latest[r.ChoreID] > r.DueOn {
			continue // superseded by a newer occurrence
		}
		if (r.DueOn < today || flex) && done && comp.CompletedAt < boardDay.Unix() {
			continue // caught up on an earlier day, or done earlier in its window
		}
		at, onTheDay := onTheDayCutoff(r.OnTheDay, r.DueOn, r.DueTime, loc, dayStart)
		onTheDay = onTheDay && !done
		if onTheDay {
			if !now.Before(at) {
				continue // too late: missed, not overdue
			}
			if b.NextChange.IsZero() || at.Before(b.NextChange) {
				b.NextChange = at
			}
		}
		card := Card{
			OccurrenceID: r.ID,
			Title:        r.Title,
			Emoji:        r.Emoji,
			DueOn:        r.DueOn,
			DueTime:      r.DueTime,
			Points:       r.Points,
			Pence:        r.Pence,
			Pool:         r.Mode == ModePool,
			Shared:       r.Mode == ModeShared,
			Overdue:      r.DueOn < today && !done,
			Done:         done,
			SortOn:       min(r.DueOn, today),
			OnTheDay:     onTheDay,
		}
		if onTheDay && r.DueTime != nil {
			card.Until = *r.DueTime
		}
		if flex && !done && r.DueOn >= today {
			days := windowDays(r.Rrule, r.StartsOn, today, r.DueOn, loc)
			card.Flex = FlexLabel(today, days)
			card.LastChance = r.DueOn == today && *r.AvailableFrom < today
		}
		if done {
			card.Pending = comp.Status == StatusPending
			card.CompletionID = comp.ID
			card.CompletedAt = comp.CompletedAt
			card.JustDone = now.Sub(time.Unix(comp.CompletedAt, 0)) < justDoneFor
		}

		owner := r.AssigneeID
		if owner == nil {
			owner = r.ClaimedBy
		}
		if owner == nil && done {
			// An unclaimed pool chore someone just did: show it under them.
			owner = &comp.PersonID
		}
		if owner == nil && card.Shared {
			// Not done yet: in each chosen person's column, as theirs to do.
			ids := shared[r.ChoreID]
			for _, id := range ids {
				col := cols[id]
				if col == nil {
					continue
				}
				c := card
				c.For = id
				for _, other := range ids {
					if p, ok := byPerson[other]; ok && other != id {
						c.With = append(c.With, p)
					}
				}
				col.Cards = append(col.Cards, c)
				col.TotalToday++
			}
			continue
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
	if d := cmp.Compare(a.SortOn, b.SortOn); d != 0 {
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

// availableFrom is the first day r is on the board.
func availableFrom(r dbq.ListBoardRow) string {
	if r.AvailableFrom != nil {
		return *r.AvailableFrom
	}
	return r.DueOn
}

// SharedPeople maps each active shared chore to its chosen, active people,
// in board order.
func (s *Service) SharedPeople(ctx context.Context, householdID int64) (map[int64][]int64, error) {
	return s.sharedPeople(ctx, householdID)
}

func (s *Service) sharedPeople(ctx context.Context, householdID int64) (map[int64][]int64, error) {
	rows, err := s.st.Q.ListChorePeople(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("chore people: %w", err)
	}
	out := map[int64][]int64{}
	for _, r := range rows {
		out[r.ChoreID] = append(out[r.ChoreID], r.PersonID)
	}
	return out, nil
}
