package chores

import (
	"context"
	"fmt"
	"slices"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// DoFor records adultID doing childID's chore for them, when the child is
// ill, away or busy. The adult earns it as an adult would (points, never
// money) and the child gets nothing, but the chore counts as done on the
// child's column. An adult did it, so it never waits for approval. It can be
// undone like any other completion.
//
// childID must be the chore's person: its assignee or claimant, or one of a
// shared chore's people. An unclaimed pool chore has no one to do it for.
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

// isTheirs checks occ is personID's to do: assigned to or claimed by them,
// or a shared chore they're one of the people for.
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
