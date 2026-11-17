package chores

import (
	"context"
	"fmt"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Summary is one person's money at a glance.
type Summary struct {
	Person                dbq.Person
	Points, Pence         int64
	HeldPoints, HeldPence int64
	Waiting               int
	Goal                  *Goal
	WeekPoints            int64
	WeekPence             int64
}

// PersonSummary returns an active person's balance, holds and this week's earnings.
func (s *Service) PersonSummary(ctx context.Context, householdID, personID int64, weekStart time.Time) (Summary, error) {
	q := s.st.Q
	p, err := getPerson(ctx, q, householdID, personID)
	if err != nil {
		return Summary{}, err
	}
	bal, err := q.BalanceForPerson(ctx, dbq.BalanceForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return Summary{}, fmt.Errorf("balance: %w", err)
	}
	held, err := q.HeldForPerson(ctx, dbq.HeldForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return Summary{}, fmt.Errorf("held: %w", err)
	}
	week, err := q.EarnedSinceForPerson(ctx, dbq.EarnedSinceForPersonParams{
		HouseholdID: householdID, PersonID: personID, SinceUnix: weekStart.Unix(),
	})
	if err != nil {
		return Summary{}, fmt.Errorf("earned this week: %w", err)
	}
	goals, err := q.ListGoals(ctx, householdID)
	if err != nil {
		return Summary{}, fmt.Errorf("goals: %w", err)
	}
	sum := Summary{
		Person: p, Points: bal.Points - held.Points, Pence: bal.Pence - held.Pence,
		HeldPoints: held.Points, HeldPence: held.Pence, Waiting: int(held.Waiting),
		WeekPoints: week.Points, WeekPence: week.Pence,
	}
	gm, err := s.loadGoalMatches(ctx, q, householdID)
	if err != nil {
		return Summary{}, err
	}
	for _, g := range goals {
		if g.PersonID == personID {
			goal := newGoal(g, sum.Points, sum.Pence)
			gm.apply(&goal, p, s.moduleOn(ctx, q, householdID, moduleMoney))
			sum.Goal = &goal
		}
	}
	if p.Kind == "adult" {
		sum.Pence, sum.HeldPence, sum.WeekPence = 0, 0, 0
	}
	return sum, nil
}
