package chores

import (
	"context"
	"fmt"
	"slices"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// DoFor records adultID doing childID's chore for them, when the child is ill, away or busy.
func (s *Service) DoFor(ctx context.Context, householdID, occurrenceID, childID, adultID int64) (dbq.Completion, error) {
	var out dbq.Completion
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		child, err := getPerson(ctx, q, householdID, childID)
		if err != nil {
			return err
		}
		if child.Kind != "child" {
			return ErrNotChild
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
		if err := isTheirs(ctx, q, chore, occ, childID); err != nil {
			return err
		}
		if s.tooLate(ctx, householdID, chore, occ) {
			return ErrTooLate
		}
		out, err = s.recordCompletionFor(ctx, q, chore, occurrenceID, adultID, &childID, StatusApproved, nil)
		if err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, adultID, "chore.completed", out.ID)
	})
	return out, err
}

func isTheirs(ctx context.Context, q *dbq.Queries, chore dbq.Chore, occ dbq.ChoreOccurrence, personID int64) error {
	owner := occ.AssigneeID
	if owner == nil {
		owner = occ.ClaimedBy
	}
	if owner != nil {
		if *owner != personID {
			return ErrNotYours
		}
		return nil
	}
	if chore.Mode != ModeShared {
		return ErrNotYours
	}
	people, err := q.ListPeopleForChore(ctx, dbq.ListPeopleForChoreParams{HouseholdID: chore.HouseholdID, ChoreID: chore.ID})
	if err != nil {
		return fmt.Errorf("chore people: %w", err)
	}
	if !slices.Contains(people, personID) {
		return ErrNotYours
	}
	return nil
}
