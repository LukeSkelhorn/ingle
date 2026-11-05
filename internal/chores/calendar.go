package chores

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// CalStatus is where a chore on the calendar stands.
type CalStatus string

// Calendar statuses.
const (
	CalUpcoming CalStatus = "upcoming" // a future day
	CalToday    CalStatus = "today"    // due on the board day, not done yet
	CalDone     CalStatus = "done"     // completed (pending or approved)
	CalMissed   CalStatus = "missed"   // an earlier day, never done
)

// CalChore is one chore on one day of the board calendar. Days within the
// materialised window come from occurrence rows; later ones are projected
// from the chore's schedule and have no OccurrenceID.
type CalChore struct {
	OccurrenceID int64 // 0 when projected
	ChoreID      int64
	Date         string // YYYY-MM-DD
	Title        string
	Emoji        string
	DueTime      *string
	Points       int64
	Pence        int64
	Pool         bool
	// PersonID is whose it is: the assignee, whoever claimed a pool chore,
	// or whoever did an unclaimed one. Nil for a pool chore up for grabs.
	PersonID *int64
	Status   CalStatus
	DoneByID *int64 // who completed it, when Done
	DoneAt   int64  // unix seconds, when Done
	Pending  bool   // done, waiting for an adult
	// Or names a flexible-day chore's days ("Tue or Thu"); it is shown on
	// each of them until it's done. "" for everything else.
	Or string
}

// Calendar returns the chores due on each local date in [from, to]
// (YYYY-MM-DD), keyed by date and in time order. Skipped occurrences are
// left out. Nothing is written: dates past the materialised window are
// projected from each chore's schedule, and nothing is projected before
// boardDay.
func (s *Service) Calendar(ctx context.Context, householdID int64, boardDay time.Time, from, to string) (map[string][]CalChore, error) {
	loc := s.loc(ctx, householdID)
	today := boardDay.Format(DateLayout)
	q := s.st.Q

	rows, err := q.ListBoard(ctx, dbq.ListBoardParams{HouseholdID: householdID, FromDate: from, ToDate: to})
	if err != nil {
		return nil, fmt.Errorf("list board: %w", err)
	}
	comps, err := q.ListLiveCompletionsForRange(ctx, dbq.ListLiveCompletionsForRangeParams{
		HouseholdID: householdID, FromDate: from, ToDate: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list completions: %w", err)
	}
	byOcc := make(map[int64]dbq.ListLiveCompletionsForRangeRow, len(comps))
	for _, c := range comps {
		byOcc[c.OccurrenceID] = c
	}

	type key struct {
		chore int64
		date  string
	}
	seen := make(map[key]bool, len(rows))
	out := map[string][]CalChore{}
	add := func(c CalChore) {
		if c.Date >= from && c.Date <= to {
			out[c.Date] = append(out[c.Date], c)
		}
	}
	for _, r := range rows {
		seen[key{r.ChoreID, r.DueOn}] = true
		if r.Status == "skipped" {
			continue
		}
		c := CalChore{
			OccurrenceID: r.ID, ChoreID: r.ChoreID, Date: r.DueOn, Title: r.Title, Emoji: r.Emoji,
			DueTime: r.DueTime, Points: r.Points, Pence: r.Pence, Pool: r.Mode == ModePool,
			PersonID: r.AssigneeID, Status: statusOn(r.DueOn, today),
		}
		if c.PersonID == nil {
			c.PersonID = r.ClaimedBy
		}
		comp, done := byOcc[r.ID]
		if done {
			c.Status, c.DoneByID, c.DoneAt, c.Pending = CalDone, &comp.PersonID, comp.CompletedAt, comp.Status == StatusPending
			if c.PersonID == nil {
				c.PersonID = &comp.PersonID
			}
		}
		if r.AvailableFrom == nil {
			add(c)
			continue
		}

		// A flexible-day chore: once done, it's on the day it was done; until
		// then, on each of its days that hasn't passed (and on its last day,
		// missed, once that has).
		days := windowDays(r.Rrule, r.StartsOn, *r.AvailableFrom, r.DueOn, loc)
		c.Or = DaysHint(days)
		if done {
			if d := time.Unix(comp.CompletedAt, 0).In(loc).Format(DateLayout); d >= *r.AvailableFrom && d <= r.DueOn {
				c.Date = d
			}
			add(c)
			continue
		}
		for _, d := range days {
			if d < today && d != r.DueOn {
				continue
			}
			c.Date, c.Status = d, statusOn(d, today)
			add(c)
		}
	}

	// Project the rest from today onwards.
	start := max(from, today)
	if start <= to {
		chores, err := q.ListChores(ctx, householdID)
		if err != nil {
			return nil, fmt.Errorf("list chores: %w", err)
		}
		first, err := time.ParseInLocation(DateLayout, start, loc)
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
		last, err := time.ParseInLocation(DateLayout, to, loc)
		if err != nil {
			return nil, fmt.Errorf("to: %w", err)
		}
		for _, ch := range chores {
			if ch.DayMode == DayAny && ch.Rrule != nil {
				// From the Monday, so a week seen from mid-week still names
				// all its days.
				ws, err := flexWindows(ch, mondayOf(first), last, loc)
				if err != nil {
					continue
				}
				for _, w := range ws {
					if seen[key{ch.ID, w.Due()}] {
						continue
					}
					or := DaysHint(w.Days)
					for _, d := range w.Days {
						if d < start {
							continue
						}
						add(CalChore{
							ChoreID: ch.ID, Date: d, Title: ch.Title, Emoji: ch.Emoji, DueTime: ch.DueTime,
							Points: ch.Points, Pence: ch.Pence, Pool: ch.Mode == ModePool,
							PersonID: ch.AssigneeID, Status: statusOn(d, today), Or: or,
						})
					}
				}
				continue
			}
			dates, err := occurrenceDates(ch, first, last, loc)
			if err != nil {
				continue // a broken rule shouldn't hide the whole calendar
			}
			for _, d := range dates {
				if d < start || d > to || seen[key{ch.ID, d}] {
					continue
				}
				out[d] = append(out[d], CalChore{
					ChoreID: ch.ID, Date: d, Title: ch.Title, Emoji: ch.Emoji, DueTime: ch.DueTime,
					Points: ch.Points, Pence: ch.Pence, Pool: ch.Mode == ModePool,
					PersonID: ch.AssigneeID, Status: statusOn(d, today),
				})
			}
		}
	}

	for d := range out {
		slices.SortStableFunc(out[d], compareCal)
	}
	return out, nil
}

// ChoreOn returns chore choreID as it falls on date, with the chore itself,
// for the calendar's read-only chore sheet. ErrNotFound means it isn't due
// that day.
func (s *Service) ChoreOn(ctx context.Context, householdID int64, boardDay time.Time, choreID int64, date string) (CalChore, dbq.Chore, error) {
	ch, err := s.st.Q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: choreID})
	if err != nil || ch.ArchivedAt != nil {
		return CalChore{}, dbq.Chore{}, ErrNotFound
	}
	day, err := s.Calendar(ctx, householdID, boardDay, date, date)
	if err != nil {
		return CalChore{}, dbq.Chore{}, err
	}
	for _, c := range day[date] {
		if c.ChoreID == choreID {
			return c, ch, nil
		}
	}
	return CalChore{}, dbq.Chore{}, ErrNotFound
}

func statusOn(date, today string) CalStatus {
	switch {
	case date < today:
		return CalMissed
	case date == today:
		return CalToday
	}
	return CalUpcoming
}

// compareCal orders a day: timed chores by time, then untimed, by title.
func compareCal(a, b CalChore) int {
	switch {
	case a.DueTime != nil && b.DueTime == nil:
		return -1
	case a.DueTime == nil && b.DueTime != nil:
		return 1
	case a.DueTime != nil && *a.DueTime != *b.DueTime:
		return cmp.Compare(*a.DueTime, *b.DueTime)
	}
	return cmp.Compare(a.Title, b.Title)
}
