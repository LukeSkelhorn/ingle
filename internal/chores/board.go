package chores

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// OverdueDays is how far back the board looks for missed chores.
const OverdueDays = 14

// UndoWindow is how long after completing a chore it can be undone from the board without an adult.
const UndoWindow = 30 * time.Second

// ErrUndoExpired is returned by Undo once the completion can no longer be undone.
var ErrUndoExpired = errors.New("too late to undo")

// Board is everything the main board shows for one board day.
type Board struct {
	Day        string
	Pool       []Card
	Columns    []Column
	People     []dbq.Person
	NextChange time.Time
	Holiday    *Holiday
}

// Column is one person's chores and balance on the board.
type Column struct {
	Person     dbq.Person
	Points     int64
	Pence      int64
	Waiting    int
	Goal       *Goal
	DoneToday  int
	TotalToday int
	Cards      []Card
	Away       *Holiday
}

// LeftToday is how many of the column's cards are still to do.
func (c Column) LeftToday() int { return c.TotalToday - c.DoneToday }

// Card is one occurrence on the board.
type Card struct {
	OccurrenceID int64
	Title        string
	Emoji        string
	DueOn        string
	DueTime      *string
	Points       int64
	Pence        int64
	AdultPoints  int64
	Pool         bool
	Claimed      bool
	Overdue      bool
	Done         bool
	Pending      bool
	CompletionID int64
	CompletedAt  int64
	JustDone     bool
	DoneBy       string
	Flex         string
	LastChance   bool
	Shared       bool
	For          int64
	With         []dbq.Person
	SortOn       string
	OnTheDay     bool
	Until        string
}

// FallbackEmoji leads a picture card when no other emoji fits.
const FallbackEmoji = "⭐"

// PictureEmoji is the emoji that leads a picture card.
func (c Card) PictureEmoji() string {
	if c.Emoji != "" {
		return c.Emoji
	}
	if e := emoji.Suggest(c.Title, emoji.Chore); e != "" {
		return e
	}
	return FallbackEmoji
}

const justDoneFor = 10 * time.Second

// Board builds the board for boardDay (the local midnight from settings.BoardDay).
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
	type copyOf struct{ chore, person int64 }
	latest := map[copyOf]string{}
	for _, r := range rows {
		k := copyOf{r.ChoreID, r.ForPerson}
		if start := availableFrom(r); start > latest[k] {
			latest[k] = start
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
	hols, err := loadHolidays(ctx, q, householdID, from)
	if err != nil {
		return Board{}, err
	}

	b := Board{Day: today, People: people}
	cols := make(map[int64]*Column, len(people))
	b.Columns = make([]Column, len(people))
	for i, p := range people {
		b.Columns[i] = Column{Person: p}
		cols[p.ID] = &b.Columns[i]
	}
	for _, h := range holidayViews(hols, people) {
		if !h.Current(today) {
			continue
		}
		at := &b.Holiday
		if h.Person != nil {
			at = &cols[h.Person.ID].Away
		}
		if *at == nil || h.EndsOn > (*at).EndsOn {
			*at = &h
		}
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
	gm, err := s.loadGoalMatches(ctx, q, householdID)
	if err != nil {
		return Board{}, err
	}
	money := s.moduleOn(ctx, q, householdID, moduleMoney)
	for _, g := range goals {
		if c := cols[g.PersonID]; c != nil {
			goal := newGoal(g, c.Points, c.Pence)
			gm.apply(&goal, c.Person, money)
			c.Goal = &goal
		}
	}
	for i := range b.Columns {
		if c := &b.Columns[i]; c.Person.Kind == "adult" {
			c.Pence = 0
		}
	}

	for _, r := range rows {
		if r.Status == "skipped" {
			continue
		}
		comp, done := byOcc[r.ID]
		if !done && hols.cleared(r.DueOn, today, rowOwners(r, shared)) {
			continue
		}
		flex := r.AvailableFrom != nil
		if r.DueOn < today && latest[copyOf{r.ChoreID, r.ForPerson}] > r.DueOn {
			continue
		}
		if (r.DueOn < today || flex) && done && comp.CompletedAt < boardDay.Unix() {
			continue
		}
		dueTime := optTime(r.DueTime)
		if !done && !flex && r.DueOn == today {
			if show, ok := appearsAt(r.DueOn, r.DueTime, r.ShowBefore, loc, dayStart); ok && now.Before(show) {
				if b.NextChange.IsZero() || show.Before(b.NextChange) {
					b.NextChange = show
				}
				continue
			}
		}
		at, onTheDay := onTheDayCutoff(r.OnTheDay, r.DueOn, dueTime, loc, dayStart)
		onTheDay = onTheDay && !done
		if onTheDay {
			if !now.Before(at) {
				continue
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
			DueTime:      dueTime,
			Points:       r.Points,
			Pence:        r.Pence,
			AdultPoints:  r.AdultPoints,
			Pool:         r.Mode == ModePool,
			Shared:       r.Mode == ModeShared,
			Overdue:      r.DueOn < today && !done,
			Done:         done,
			SortOn:       min(r.DueOn, today),
			OnTheDay:     onTheDay,
		}
		if onTheDay && dueTime != nil {
			card.Until = *dueTime
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
			if comp.ForPersonID != nil {
				card.DoneBy = byPerson[comp.PersonID].Name
			}
		}

		owner := r.AssigneeID
		if owner == nil {
			owner = r.ClaimedBy
		}
		if owner == nil && done {
			owner = &comp.PersonID
			if comp.ForPersonID != nil {
				owner = comp.ForPersonID
			}
		}
		if owner == nil && card.Shared {
			ids := shared[r.ChoreID]
			for _, id := range ids {
				col := cols[id]
				if col == nil || hols.awayOn(today, []int64{id}) {
					continue
				}
				c := card
				c.For = id
				c.Points, c.Pence = Earns(col.Person.Kind, r.Points, r.Pence, r.AdultPoints)
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
			card.Pool = true
			b.Pool = append(b.Pool, card)
			continue
		}
		col := cols[*owner]
		if col == nil {
			continue
		}
		card.Claimed = card.Pool && !done
		card.Points, card.Pence = Earns(col.Person.Kind, r.Points, r.Pence, r.AdultPoints)
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

// Undo reverses a board tap within UndoWindow, if no adult has reviewed it.
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
		if err := q.DeleteMatchForCompletion(ctx, dbq.DeleteMatchForCompletionParams{
			HouseholdID: householdID, CompletionID: &c.ID,
		}); err != nil {
			return fmt.Errorf("delete match: %w", err)
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

func availableFrom(r dbq.ListBoardRow) string {
	if r.AvailableFrom != nil {
		return *r.AvailableFrom
	}
	return r.DueOn
}

// SharedPeople maps each active shared chore to its chosen, active people, in board order.
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
