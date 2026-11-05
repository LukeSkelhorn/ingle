package chores

import (
	"context"
	"fmt"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// Summary is one person's money at a glance: their balance, and what they've
// earned since the start of the week.
type Summary struct {
	Person        dbq.Person
	Points, Pence int64 // balance: every ledger entry
	WeekPoints    int64 // earned since weekStart, net of reversals
	WeekPence     int64
}

// PersonSummary returns the balance and this week's earnings for an active
// person in the household. weekStart is the local midnight the week began
// (locale.StartOfWeek). Spending on rewards and payouts lowers the balance
// but not what they've earned this week. It returns ErrNotFound for an
// archived person or one from another household.
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
	week, err := q.EarnedSinceForPerson(ctx, dbq.EarnedSinceForPersonParams{
		HouseholdID: householdID, PersonID: personID, SinceUnix: weekStart.Unix(),
	})
	if err != nil {
		return Summary{}, fmt.Errorf("earned this week: %w", err)
	}
	return Summary{
		Person: p, Points: bal.Points, Pence: bal.Pence,
		WeekPoints: week.Points, WeekPence: week.Pence,
	}, nil
}
