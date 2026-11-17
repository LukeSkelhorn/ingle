package chores

import (
	"context"
	"fmt"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// MaxHolidayDays is the longest holiday that can be added.
const MaxHolidayDays = 366

// Holiday is one holiday, for Admin › Settings and the board.
type Holiday struct {
	ID       int64
	Person   *dbq.Person
	StartsOn string
	EndsOn   string
}

// Current reports whether today is one of h's days.
func (h Holiday) Current(today string) bool { return h.StartsOn <= today && today <= h.EndsOn }

// Dates is h's dates for a list, e.g. "Sat 2 Aug – Sun 10 Aug", or one date for a single day.
func (h Holiday) Dates(today string) string {
	from := dayLabel(h.StartsOn, today)
	if h.EndsOn == h.StartsOn {
		return from
	}
	return from + " – " + dayLabel(h.EndsOn, today)
}

// Until says when h ends, for the board.
func (h Holiday) Until(today string) string {
	end, err1 := time.Parse(DateLayout, h.EndsOn)
	now, err2 := time.Parse(DateLayout, today)
	switch {
	case err1 != nil || err2 != nil:
		return ""
	case h.EndsOn <= today:
		return "today"
	case end.Sub(now) < 7*24*time.Hour:
		return "until " + end.Format("Monday")
	}
	return "until " + dayLabel(h.EndsOn, today)
}

func dayLabel(date, today string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	if len(today) >= 4 && date[:4] != today[:4] {
		return t.Format("Mon 2 Jan 2006")
	}
	return t.Format("Mon 2 Jan")
}

type holidays []dbq.Holiday

func loadHolidays(ctx context.Context, q *dbq.Queries, householdID int64, from string) (holidays, error) {
	rows, err := q.ListHolidays(ctx, dbq.ListHolidaysParams{HouseholdID: householdID, FromDate: from})
	if err != nil {
		return nil, fmt.Errorf("list holidays: %w", err)
	}
	return rows, nil
}

func (hs holidays) awayOn(date string, owners []int64) bool {
	return hs.any(owners, func(h dbq.Holiday) bool { return h.StartsOn <= date && date <= h.EndsOn })
}

func (hs holidays) awayAll(dates []string, owners []int64) bool {
	for _, d := range dates {
		if !hs.awayOn(d, owners) {
			return false
		}
	}
	return len(dates) > 0
}

func (hs holidays) cleared(dueOn, today string, owners []int64) bool {
	return hs.any(owners, func(h dbq.Holiday) bool { return h.StartsOn <= today && h.EndsOn >= dueOn })
}

func (hs holidays) any(owners []int64, match func(dbq.Holiday) bool) bool {
	for _, h := range hs {
		if h.PersonID == nil && match(h) {
			return true
		}
	}
	if len(owners) == 0 {
		return false
	}
	for _, o := range owners {
		found := false
		for _, h := range hs {
			if h.PersonID != nil && *h.PersonID == o && match(h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func slotOwners(c dbq.Chore, sl slot, who doers) []int64 {
	switch {
	case sl.assignee != nil:
		return []int64{*sl.assignee}
	case c.Mode == ModeShared:
		return who.chosen[c.ID]
	}
	return nil
}

func rowOwners(r dbq.ListBoardRow, shared map[int64][]int64) []int64 {
	switch {
	case r.AssigneeID != nil:
		return []int64{*r.AssigneeID}
	case r.ClaimedBy != nil:
		return []int64{*r.ClaimedBy}
	case r.Mode == ModeShared:
		return shared[r.ChoreID]
	}
	return nil
}

// HolidayInput is a holiday being added: PersonID nil for everyone.
type HolidayInput struct {
	PersonID *int64
	From, To string
}

// Holidays lists the household's holidays that haven't ended by boardDay, soonest first.
func (s *Service) Holidays(ctx context.Context, householdID int64, boardDay time.Time) ([]Holiday, error) {
	q := s.st.Q
	rows, err := loadHolidays(ctx, q, householdID, boardDay.Format(DateLayout))
	if err != nil {
		return nil, err
	}
	people, err := q.ListPeople(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	return holidayViews(rows, people), nil
}

func holidayViews(rows holidays, people []dbq.Person) []Holiday {
	byID := make(map[int64]dbq.Person, len(people))
	for _, p := range people {
		byID[p.ID] = p
	}
	out := make([]Holiday, 0, len(rows))
	for _, r := range rows {
		h := Holiday{ID: r.ID, StartsOn: r.StartsOn, EndsOn: r.EndsOn}
		if r.PersonID != nil {
			p, ok := byID[*r.PersonID]
			if !ok {
				continue
			}
			h.Person = &p
		}
		out = append(out, h)
	}
	return out
}

// AddHoliday adds a holiday and takes the chores it pauses off the board from boardDay.
func (s *Service) AddHoliday(ctx context.Context, householdID, actorID int64, in HolidayInput, boardDay time.Time) (int64, error) {
	today := boardDay.Format(DateLayout)
	errs := valid.Errors{}
	from, err1 := time.Parse(DateLayout, in.From)
	to, err2 := time.Parse(DateLayout, in.To)
	switch {
	case err1 != nil:
		errs.Add("holiday_from", "Choose the first day.")
	case err2 != nil:
		errs.Add("holiday_to", "Choose the last day.")
	case to.Before(from):
		errs.Add("holiday_to", "The last day can't be before the first.")
	case in.To < today:
		errs.Add("holiday_to", "That holiday has already finished.")
	case to.Sub(from) >= MaxHolidayDays*24*time.Hour:
		errs.Add("holiday_to", "Holidays can be up to a year long.")
	}
	var id int64
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if in.PersonID != nil {
			if _, err := getPerson(ctx, q, householdID, *in.PersonID); err != nil {
				errs.Add("holiday_person", "Choose someone in the household.")
			}
		}
		if err := errs.Err(); err != nil {
			return err
		}
		var err error
		id, err = q.InsertHoliday(ctx, dbq.InsertHolidayParams{
			HouseholdID: householdID, PersonID: in.PersonID, StartsOn: in.From, EndsOn: in.To,
			CreatedAt: s.now().Unix(),
		})
		if err != nil {
			return fmt.Errorf("insert holiday: %w", err)
		}
		if err := s.pause(ctx, q, householdID, today); err != nil {
			return err
		}
		return s.logHoliday(ctx, q, householdID, actorID, "holiday.added", id)
	})
	return id, err
}

// EndHoliday ends a holiday now.
func (s *Service) EndHoliday(ctx context.Context, householdID, actorID, id int64, boardDay time.Time) error {
	today := boardDay.Format(DateLayout)
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		h, err := q.GetHoliday(ctx, dbq.GetHolidayParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if h.StartsOn >= today {
			if err := q.DeleteHoliday(ctx, dbq.DeleteHolidayParams{HouseholdID: householdID, ID: id}); err != nil {
				return fmt.Errorf("delete holiday: %w", err)
			}
		} else if h.EndsOn >= today {
			yesterday := boardDay.AddDate(0, 0, -1).Format(DateLayout)
			if err := q.SetHolidayEnd(ctx, dbq.SetHolidayEndParams{EndsOn: yesterday, HouseholdID: householdID, ID: id}); err != nil {
				return fmt.Errorf("end holiday: %w", err)
			}
		}
		return s.logHoliday(ctx, q, householdID, actorID, "holiday.ended", id)
	})
	if err != nil {
		return err
	}
	return s.resume(ctx, householdID, boardDay)
}

// RemoveHoliday deletes a holiday, and its chores from boardDay come back.
func (s *Service) RemoveHoliday(ctx context.Context, householdID, actorID, id int64, boardDay time.Time) error {
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetHoliday(ctx, dbq.GetHolidayParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		if err := q.DeleteHoliday(ctx, dbq.DeleteHolidayParams{HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("delete holiday: %w", err)
		}
		return s.logHoliday(ctx, q, householdID, actorID, "holiday.removed", id)
	})
	if err != nil {
		return err
	}
	return s.resume(ctx, householdID, boardDay)
}

func (s *Service) resume(ctx context.Context, householdID int64, boardDay time.Time) error {
	if _, err := s.Materialise(ctx, householdID, boardDay, Window); err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	return nil
}

func (s *Service) pause(ctx context.Context, q *dbq.Queries, householdID int64, today string) error {
	hols, err := loadHolidays(ctx, q, householdID, today)
	if err != nil {
		return err
	}
	rows, err := q.ListBoard(ctx, dbq.ListBoardParams{HouseholdID: householdID, FromDate: today, ToDate: maxDate})
	if err != nil {
		return fmt.Errorf("list board: %w", err)
	}
	people, err := q.ListChorePeople(ctx, householdID)
	if err != nil {
		return fmt.Errorf("chore people: %w", err)
	}
	shared := map[int64][]int64{}
	for _, p := range people {
		shared[p.ChoreID] = append(shared[p.ChoreID], p.PersonID)
	}
	loc := s.loc(ctx, householdID)
	for _, r := range rows {
		if r.Status != StatusOpen && r.Status != StatusClaimed {
			continue
		}
		days := []string{r.DueOn}
		if r.AvailableFrom != nil {
			days = windowDays(r.Rrule, r.StartsOn, max(*r.AvailableFrom, today), r.DueOn, loc)
		}
		if r.ClaimedBy != nil && hols.awayAll(days, []int64{*r.ClaimedBy}) {
			if _, err := q.ReleaseOccurrence(ctx, dbq.ReleaseOccurrenceParams{
				HouseholdID: householdID, ID: r.ID, PersonID: r.ClaimedBy,
			}); err != nil {
				return fmt.Errorf("release occurrence: %w", err)
			}
			r.ClaimedBy = nil
		}
		if r.ClaimedBy != nil || !hols.awayAll(days, rowOwners(r, shared)) {
			continue
		}
		if err := q.DeleteOpenOccurrence(ctx, dbq.DeleteOpenOccurrenceParams{HouseholdID: householdID, ID: r.ID}); err != nil {
			return fmt.Errorf("delete occurrence: %w", err)
		}
	}
	return nil
}

const maxDate = "9999-12-31"

func (s *Service) logHoliday(ctx context.Context, q *dbq.Queries, householdID, actorID int64, verb string, id int64) error {
	return store.LogActivity(ctx, q, store.Activity{
		HouseholdID: householdID, ActorID: &actorID, Verb: verb,
		SubjectType: "holiday", SubjectID: id, At: s.now(),
	})
}
