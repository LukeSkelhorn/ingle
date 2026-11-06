package chores

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// ErrTooLate is returned when completing or claiming an Only on the day
// chore after its time has passed.
var ErrTooLate = errors.New("too late for that chore")

// cutoff is when an Only on the day occurrence due on dueOn (its last
// allowed day, for a flexible-day chore) stops being doable: its due time
// if it has one, else the start of the next board day. A due time earlier
// than dayStart belongs to the end of the board day, so the next calendar
// day. Clock times are wall-clock, so days when the clocks change still
// cut off at the right local time.
func cutoff(dueOn string, dueTime *string, loc *time.Location, dayStart time.Duration) (time.Time, bool) {
	d, err := time.ParseInLocation(DateLayout, dueOn, loc)
	if err != nil {
		return time.Time{}, false
	}
	at := dayStart
	next := true
	if dueTime != nil {
		t, err := time.Parse("15:04", *dueTime)
		if err != nil {
			return time.Time{}, false
		}
		at = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
		next = at < dayStart
	}
	if next {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), int(at/time.Hour), int(at%time.Hour/time.Minute), 0, 0, loc), true
}

// onTheDayCutoff is the cutoff of an occurrence due on dueOn of a chore with
// the given on_the_day flag and due time, and false when it can be done late.
func onTheDayCutoff(onTheDay int64, dueOn string, dueTime *string, loc *time.Location, dayStart time.Duration) (time.Time, bool) {
	if onTheDay != 1 {
		return time.Time{}, false
	}
	return cutoff(dueOn, dueTime, loc, dayStart)
}

// tooLate reports whether occurrence occ of chore c is past its cutoff.
func (s *Service) tooLate(ctx context.Context, householdID int64, c dbq.Chore, occ dbq.ChoreOccurrence) bool {
	at, ok := onTheDayCutoff(c.OnTheDay, occ.DueOn, c.DueTime, s.loc(ctx, householdID), s.dayStart(ctx, householdID))
	return ok && !s.now().Before(at)
}

// MissedDays is how far back Admin › Completions lists missed chores,
// counting today.
const MissedDays = 7

// MissedDetail is an Only on the day chore that wasn't done in time.
type MissedDetail struct {
	OccurrenceID int64
	Title        string
	Emoji        string
	DueOn        string
	DueTime      *string
	Points       int64
	Pence        int64
	// Person is whose it was (the assignee, or whoever claimed it); nil
	// when Choices lists who could have done it (a shared chore's people,
	// or everyone for an unclaimed pool chore).
	Person  *dbq.Person
	Choices []dbq.Person
}

// Missed lists Only on the day chores from the last MissedDays board days
// (up to boardDay) that are past their cutoff and weren't done, newest first.
func (s *Service) Missed(ctx context.Context, householdID int64, boardDay time.Time) ([]MissedDetail, error) {
	q := s.st.Q
	today := boardDay.Format(DateLayout)
	from := boardDay.AddDate(0, 0, 1-MissedDays).Format(DateLayout)
	rows, err := q.ListBoard(ctx, dbq.ListBoardParams{HouseholdID: householdID, FromDate: from, ToDate: today})
	if err != nil {
		return nil, fmt.Errorf("list board: %w", err)
	}
	people, err := q.ListPeople(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	shared, err := s.sharedPeople(ctx, householdID)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]dbq.Person, len(people))
	for _, p := range people {
		byID[p.ID] = p
	}
	loc, dayStart, now := s.loc(ctx, householdID), s.dayStart(ctx, householdID), s.now()
	var out []MissedDetail
	for _, r := range rows {
		if r.Status != StatusOpen && r.Status != StatusClaimed {
			continue
		}
		at, ok := onTheDayCutoff(r.OnTheDay, r.DueOn, r.DueTime, loc, dayStart)
		if !ok || now.Before(at) {
			continue
		}
		m := MissedDetail{
			OccurrenceID: r.ID, Title: r.Title, Emoji: r.Emoji, DueOn: r.DueOn, DueTime: r.DueTime,
			Points: r.Points, Pence: r.Pence,
		}
		owner := r.AssigneeID
		if owner == nil {
			owner = r.ClaimedBy
		}
		switch {
		case owner != nil:
			p, ok := byID[*owner]
			if !ok {
				continue // archived since
			}
			m.Person = &p
		case r.Mode == ModeShared:
			for _, id := range shared[r.ChoreID] {
				if p, ok := byID[id]; ok {
					m.Choices = append(m.Choices, p)
				}
			}
		default:
			m.Choices = people
		}
		if m.Person == nil && len(m.Choices) == 0 {
			continue
		}
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b MissedDetail) int {
		if d := cmp.Compare(b.DueOn, a.DueOn); d != 0 {
			return d
		}
		return compareTimes(b.DueTime, a.DueTime)
	})
	return out, nil
}

// compareTimes orders optional HH:MM times, untimed last.
func compareTimes(a, b *string) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp.Compare(*a, *b)
}

// MarkDone records an adult (adultID) marking a not-done occurrence as done
// by personID, for a missed Only on the day chore that was done but never
// ticked. It is approved straight away, with the adult as reviewer, so the
// points are added even if the chore normally needs checking.
func (s *Service) MarkDone(ctx context.Context, householdID, occurrenceID, personID, adultID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		occ, err := getOccurrence(ctx, q, householdID, occurrenceID)
		if err != nil {
			return err
		}
		if occ.Status == StatusDone {
			return ErrNotOpen
		}
		chore, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: occ.ChoreID})
		if err != nil {
			return fmt.Errorf("get chore: %w", err)
		}
		if err := canDo(ctx, q, chore, occ, personID); err != nil {
			return err
		}
		c, err := s.recordCompletion(ctx, q, chore, occurrenceID, personID, StatusPending, nil)
		if err != nil {
			return err
		}
		return s.reviewTx(ctx, q, householdID, c.ID, adultID, StatusApproved)
	})
}

// ErrNotMissed is returned when dismissing a chore that isn't a missed Only
// on the day chore (it was done, dismissed already, or isn't late yet), or
// restoring one that isn't dismissed.
var ErrNotMissed = errors.New("chore isn't missed")

// Dismiss takes missed Only on the day chores off Admin › Completions, as
// adult adultID. They stay missed: no points, and the calendar still shows
// them as missed. It returns the ids it dismissed, skipping any that aren't
// missed (done or dismissed since the page was loaded); ErrNotMissed if
// none were.
func (s *Service) Dismiss(ctx context.Context, householdID, adultID int64, occurrenceIDs []int64) ([]int64, error) {
	var done []int64
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		for _, id := range occurrenceIDs {
			occ, err := getOccurrence(ctx, q, householdID, id)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			chore, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: occ.ChoreID})
			if err != nil {
				return fmt.Errorf("get chore: %w", err)
			}
			if !s.tooLate(ctx, householdID, chore, occ) {
				continue
			}
			n, err := q.DismissOccurrence(ctx, dbq.DismissOccurrenceParams{HouseholdID: householdID, ID: id})
			if err != nil {
				return fmt.Errorf("dismiss occurrence: %w", err)
			}
			if n == 1 {
				done = append(done, id)
			}
		}
		if len(done) == 0 {
			return ErrNotMissed
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}

// Restore undoes Dismiss for the given occurrences, as adult adultID,
// putting them back on the Missed list. It returns how many it restored;
// ErrNotMissed if none were dismissed.
func (s *Service) Restore(ctx context.Context, householdID, adultID int64, occurrenceIDs []int64) (int, error) {
	n := 0
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		for _, id := range occurrenceIDs {
			r, err := q.RestoreOccurrence(ctx, dbq.RestoreOccurrenceParams{HouseholdID: householdID, ID: id})
			if err != nil {
				return fmt.Errorf("restore occurrence: %w", err)
			}
			n += int(r)
		}
		if n == 0 {
			return ErrNotMissed
		}
		return nil
	})
	return n, err
}
