package chores

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Input is the editable part of a chore, as submitted by the admin form.
type Input struct {
	Title            string
	Emoji            string // a single emoji, or "" for none
	Description      string
	Mode             string  // ModeAssigned or ModePool
	AssigneeID       *int64  // required for ModeAssigned, ignored otherwise
	PeopleIDs        []int64 // ModeShared: who it's for (two or more)
	Repeat           Recurrence
	DayMode          string   // DayEach or DayAny; "" means DayEach
	StartsOn         string   // YYYY-MM-DD
	DueTimes         []string // HH:MM, any order; blanks are ignored. Empty for an untimed chore
	ShowBefore       *int64   // minutes before each time it appears on the board; nil for all day
	Points           int64
	Pence            int64
	RequiresApproval bool
	OnTheDay         bool // it can't be done late: off the board once its time has passed
}

// Chores returns active chores, by title.
func (s *Service) Chores(ctx context.Context, householdID int64) ([]dbq.Chore, error) {
	return s.st.Q.ListChores(ctx, householdID)
}

// Chore returns one chore.
func (s *Service) Chore(ctx context.Context, householdID, id int64) (dbq.Chore, error) {
	c, err := s.st.Q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: id})
	return c, notFound(err)
}

// CreateChore adds a chore and materialises its occurrences from boardDay
// (the household's current day) for the usual Window.
func (s *Service) CreateChore(ctx context.Context, householdID, actorID int64, in Input, boardDay time.Time) (dbq.Chore, error) {
	var c dbq.Chore
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		p, err := s.validate(ctx, q, householdID, in)
		if err != nil {
			return err
		}
		c, err = q.CreateChore(ctx, dbq.CreateChoreParams{
			HouseholdID: householdID, Title: p.Title, Emoji: p.Emoji, Description: p.Description,
			Mode: p.Mode, AssigneeID: p.AssigneeID, Rrule: p.Rrule, StartsOn: p.StartsOn,
			DueTimes: p.DueTimes, ShowBefore: p.ShowBefore, Points: p.Points, Pence: p.Pence, RequiresApproval: p.RequiresApproval,
			CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create chore: %w", err)
		}
		if c, err = setDayMode(ctx, q, c, p.DayMode); err != nil {
			return err
		}
		if c, err = setOnTheDay(ctx, q, c, in.OnTheDay); err != nil {
			return err
		}
		if err := setPeople(ctx, q, c, p.PeopleIDs); err != nil {
			return err
		}
		if err := s.refresh(ctx, q, c, nil, boardDay); err != nil {
			return err
		}
		return s.logAdmin(ctx, q, householdID, actorID, "chore.created", c.ID)
	})
	return c, err
}

// UpdateChore edits a chore. Its not-yet-started occurrences from boardDay
// on are replaced so a new schedule or assignee shows up straight away;
// claimed, done and previously completed occurrences are kept.
func (s *Service) UpdateChore(ctx context.Context, householdID, actorID, id int64, in Input, boardDay time.Time) (dbq.Chore, error) {
	var c dbq.Chore
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		old, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if old.ArchivedAt != nil {
			return ErrNotFound
		}
		p, err := s.validate(ctx, q, householdID, in)
		if err != nil {
			return err
		}
		c, err = q.UpdateChore(ctx, dbq.UpdateChoreParams{
			Title: p.Title, Emoji: p.Emoji, Description: p.Description, Mode: p.Mode, AssigneeID: p.AssigneeID,
			Rrule: p.Rrule, StartsOn: p.StartsOn, DueTimes: p.DueTimes, ShowBefore: p.ShowBefore, Points: p.Points,
			Pence: p.Pence, RequiresApproval: p.RequiresApproval, UpdatedAt: now.Unix(),
			HouseholdID: householdID, ID: id,
		})
		if err != nil {
			return fmt.Errorf("update chore: %w", err)
		}
		if c, err = setDayMode(ctx, q, c, p.DayMode); err != nil {
			return err
		}
		if c, err = setOnTheDay(ctx, q, c, in.OnTheDay); err != nil {
			return err
		}
		if err := setPeople(ctx, q, c, p.PeopleIDs); err != nil {
			return err
		}
		if err := s.refresh(ctx, q, c, &old, boardDay); err != nil {
			return err
		}
		return s.logAdmin(ctx, q, householdID, actorID, "chore.updated", id)
	})
	return c, err
}

// ArchiveChore hides a chore and drops its not-yet-started occurrences.
func (s *Service) ArchiveChore(ctx context.Context, householdID, actorID, id int64, boardDay time.Time) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		c, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if c.ArchivedAt != nil {
			return nil
		}
		if err := q.ArchiveChore(ctx, dbq.ArchiveChoreParams{
			ArchivedAt: &[]int64{now.Unix()}[0], UpdatedAt: now.Unix(), HouseholdID: householdID, ID: id,
		}); err != nil {
			return fmt.Errorf("archive chore: %w", err)
		}
		if _, err := q.DeleteFutureOpenOccurrences(ctx, dbq.DeleteFutureOpenOccurrencesParams{
			HouseholdID: householdID, ChoreID: id, FromDate: clearFrom(c, nil, boardDay),
		}); err != nil {
			return fmt.Errorf("clear occurrences: %w", err)
		}
		return s.logAdmin(ctx, q, householdID, actorID, "chore.archived", id)
	})
}

// setDayMode stores c's day mode when it differs.
func setDayMode(ctx context.Context, q *dbq.Queries, c dbq.Chore, mode string) (dbq.Chore, error) {
	if c.DayMode == mode {
		return c, nil
	}
	c, err := q.SetChoreDayMode(ctx, dbq.SetChoreDayModeParams{DayMode: mode, HouseholdID: c.HouseholdID, ID: c.ID})
	if err != nil {
		return c, fmt.Errorf("set day mode: %w", err)
	}
	return c, nil
}

// setOnTheDay stores whether c is Only on the day, when it differs.
func setOnTheDay(ctx context.Context, q *dbq.Queries, c dbq.Chore, on bool) (dbq.Chore, error) {
	v := int64(0)
	if on {
		v = 1
	}
	if c.OnTheDay == v {
		return c, nil
	}
	c, err := q.SetChoreOnTheDay(ctx, dbq.SetChoreOnTheDayParams{OnTheDay: v, HouseholdID: c.HouseholdID, ID: c.ID})
	if err != nil {
		return c, fmt.Errorf("set on the day: %w", err)
	}
	return c, nil
}

// setPeople replaces who a shared chore is for; other chores have nobody.
func setPeople(ctx context.Context, q *dbq.Queries, c dbq.Chore, people []int64) error {
	if err := q.DeleteChorePeople(ctx, dbq.DeleteChorePeopleParams{HouseholdID: c.HouseholdID, ChoreID: c.ID}); err != nil {
		return fmt.Errorf("clear chore people: %w", err)
	}
	if c.Mode != ModeShared {
		return nil
	}
	for _, id := range people {
		if err := q.AddChorePerson(ctx, dbq.AddChorePersonParams{HouseholdID: c.HouseholdID, ChoreID: c.ID, PersonID: id}); err != nil {
			return fmt.Errorf("add chore person: %w", err)
		}
	}
	return nil
}

// ChorePeople returns who a shared chore is for (active people only).
func (s *Service) ChorePeople(ctx context.Context, householdID, choreID int64) ([]int64, error) {
	return s.st.Q.ListPeopleForChore(ctx, dbq.ListPeopleForChoreParams{HouseholdID: householdID, ChoreID: choreID})
}

// refresh replaces c's not-yet-started occurrences and re-materialises the
// window from boardDay. old is the chore before an edit (nil on create).
func (s *Service) refresh(ctx context.Context, q *dbq.Queries, c dbq.Chore, old *dbq.Chore, boardDay time.Time) error {
	if old != nil {
		if _, err := q.DeleteFutureOpenOccurrences(ctx, dbq.DeleteFutureOpenOccurrencesParams{
			HouseholdID: c.HouseholdID, ChoreID: c.ID, FromDate: clearFrom(c, old, boardDay),
		}); err != nil {
			return fmt.Errorf("clear occurrences: %w", err)
		}
	}
	loc := s.loc(ctx, c.HouseholdID)
	start, end := window(boardDay, Window, loc)
	_, err := materialiseChore(ctx, q, c, start, end, loc)
	return err
}

// clearFrom is the first date whose open occurrences are replaced. One-off
// chores keep their single occurrence even when it is overdue, so if either
// version is one-off every open occurrence is cleared (otherwise moving an
// overdue one-off would leave the old one behind).
func clearFrom(c dbq.Chore, old *dbq.Chore, boardDay time.Time) string {
	if c.Rrule == nil || (old != nil && old.Rrule == nil) {
		return "0001-01-01"
	}
	return boardDay.Format(DateLayout)
}

type validated struct {
	Title, Emoji, Description, Mode, StartsOn string
	DayMode                                   string
	AssigneeID                                *int64
	PeopleIDs                                 []int64
	Rrule                                     *string
	DueTimes                                  string
	ShowBefore                                *int64
	Points, Pence, RequiresApproval           int64
}

func (s *Service) validate(ctx context.Context, q *dbq.Queries, householdID int64, in Input) (validated, error) {
	errs := valid.Errors{}
	out := validated{
		Title:       strings.TrimSpace(in.Title),
		Emoji:       strings.TrimSpace(in.Emoji),
		Description: strings.TrimSpace(in.Description),
		Mode:        in.Mode,
		StartsOn:    in.StartsOn,
		ShowBefore:  in.ShowBefore,
		Points:      in.Points,
		Pence:       in.Pence,
	}
	if in.RequiresApproval {
		out.RequiresApproval = 1
	}
	if n := len([]rune(out.Title)); n < 1 || n > 100 {
		errs.Add("title", "Enter a title (up to 100 characters).")
	}
	if out.Emoji != "" && !emoji.Valid(out.Emoji) {
		errs.Add("emoji", "Use a single emoji.")
	}
	if len(out.Description) > 1000 {
		errs.Add("description", "Keep it under 1,000 characters.")
	}
	switch in.Mode {
	case ModeAssigned:
		if in.AssigneeID == nil {
			errs.Add("assignee_id", "Choose who it's for.")
		} else if p, err := getPerson(ctx, q, householdID, *in.AssigneeID); err != nil {
			errs.Add("assignee_id", "Choose who it's for.")
		} else {
			out.AssigneeID = &p.ID
		}
	case ModePool:
	case ModeShared:
		// Distinct, active people. One is just an assigned chore.
		for _, id := range in.PeopleIDs {
			if slices.Contains(out.PeopleIDs, id) {
				continue
			}
			if p, err := getPerson(ctx, q, householdID, id); err == nil {
				out.PeopleIDs = append(out.PeopleIDs, p.ID)
			}
		}
		switch len(out.PeopleIDs) {
		case 0:
			errs.Add("people_ids", "Choose at least two people.")
		case 1:
			out.Mode, out.AssigneeID, out.PeopleIDs = ModeAssigned, &out.PeopleIDs[0], nil
		}
	default:
		errs.Add("mode", "Choose who does it.")
	}
	rule, err := in.Repeat.RRule()
	if err != nil {
		field := "repeat"
		switch {
		case errors.Is(err, ErrNoDays):
			field = "days"
		case errors.Is(err, ErrBadMonthly):
			field = "month_day"
		}
		errs.Add(field, sentence(err.Error()))
	} else if rule != nil {
		if err := ValidateRRule(*rule); err != nil {
			errs.Add("repeat", "That schedule isn't valid.")
		}
	}
	out.Rrule = rule
	switch in.DayMode {
	case "", DayEach:
		out.DayMode = DayEach
	case DayAny:
		// Only weekly-style schedules have days to choose between; for the
		// rest the choice is hidden, so it quietly means each.
		out.DayMode = DayEach
		if AnyDayAllowed(in.Repeat.Kind) {
			out.DayMode = DayAny
		}
	default:
		errs.Add("day_mode", "Choose how the days work.")
	}
	if _, err := time.Parse(DateLayout, in.StartsOn); err != nil {
		errs.Add("starts_on", "Choose a start date.")
	}
	var ts []string
	for _, t := range in.DueTimes {
		t = strings.TrimSpace(t)
		if t == "" || slices.Contains(ts, t) {
			continue
		}
		if _, err := time.Parse("15:04", t); err != nil || len(t) != 5 {
			errs.Add("due_time", "Enter times like 17:30, or leave them blank.")
			continue
		}
		ts = append(ts, t)
	}
	slices.Sort(ts)
	switch {
	case len(ts) > MaxTimes:
		errs.Add("due_time", fmt.Sprintf("Use up to %d times a day.", MaxTimes))
	case len(ts) > 1 && out.DayMode == DayAny:
		errs.Add("due_time", "A chore done on any one of its days can have one time.")
	}
	out.DueTimes = strings.Join(ts, ",")
	if in.ShowBefore != nil && !slices.Contains(ShowBeforeOptions, *in.ShowBefore) {
		errs.Add("show_before", "Choose when it shows on the board.")
	}
	if in.Points < 0 || in.Points > 100000 {
		errs.Add("points", "Use 0 to 100,000 points.")
	}
	if in.Pence < 0 || in.Pence > 100000 {
		errs.Add("pence", "Use £0 to £1,000.")
	}
	return out, errs.Err()
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func (s *Service) logAdmin(ctx context.Context, q *dbq.Queries, householdID, actorID int64, verb string, choreID int64) error {
	return store.LogActivity(ctx, q, store.Activity{
		HouseholdID: householdID, ActorID: &actorID, Verb: verb,
		SubjectType: "chore", SubjectID: choreID, At: s.now(),
	})
}
