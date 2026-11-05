package chores

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lukeskelhorn/domus/internal/emoji"
	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
	"github.com/lukeskelhorn/domus/internal/valid"
)

// Input is the editable part of a chore, as submitted by the admin form.
type Input struct {
	Title            string
	Emoji            string // a single emoji, or "" for none
	Description      string
	Mode             string // ModeAssigned or ModePool
	AssigneeID       *int64 // required for ModeAssigned, ignored for ModePool
	Repeat           Recurrence
	StartsOn         string  // YYYY-MM-DD
	DueTime          *string // HH:MM, optional
	Points           int64
	Pence            int64
	RequiresApproval bool
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
			DueTime: p.DueTime, Points: p.Points, Pence: p.Pence, RequiresApproval: p.RequiresApproval,
			CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create chore: %w", err)
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
			Rrule: p.Rrule, StartsOn: p.StartsOn, DueTime: p.DueTime, Points: p.Points,
			Pence: p.Pence, RequiresApproval: p.RequiresApproval, UpdatedAt: now.Unix(),
			HouseholdID: householdID, ID: id,
		})
		if err != nil {
			return fmt.Errorf("update chore: %w", err)
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
	AssigneeID                                *int64
	Rrule, DueTime                            *string
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
		DueTime:     in.DueTime,
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
	default:
		errs.Add("mode", "Choose assigned or shared pool.")
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
	if _, err := time.Parse(DateLayout, in.StartsOn); err != nil {
		errs.Add("starts_on", "Choose a start date.")
	}
	if in.DueTime != nil {
		if _, err := time.Parse("15:04", *in.DueTime); err != nil || len(*in.DueTime) != 5 {
			errs.Add("due_time", "Enter a time like 17:30, or leave it blank.")
		}
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
