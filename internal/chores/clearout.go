package chores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Cleared counts what one household's clear-out removed.
type Cleared struct {
	Folded      int64 // ledger lines removed, their total added to a Brought forward line
	Completions int64
	Occurrences int64
	Redemptions int64
	Transfers   int64
	Activity    int64
}

// Any reports whether anything was removed.
func (c Cleared) Any() bool {
	return c.Folded+c.Completions+c.Occurrences+c.Redemptions+c.Transfers+c.Activity > 0
}

// HistoryCutoff is the start of the day months ago in loc: a clear-out
// keeps everything from then on.
func HistoryCutoff(now time.Time, loc *time.Location, months int) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m-time.Month(months), d, 0, 0, 0, 0, loc)
}

// ClearHistory clears out the household's history from before its Keep
// history cutoff; with Keep history at Everything it does nothing. Points
// and money don't change: each person's ledger lines from before the cutoff
// are added up into one Brought forward line. Interest and match lines stay,
// as do claims, bank requests and chores still waiting for a grown-up.
// Running it again the same day changes nothing.
func (s *Service) ClearHistory(ctx context.Context, householdID int64) (Cleared, error) {
	hs, err := s.st.Q.GetSettings(ctx, householdID)
	if errors.Is(err, sql.ErrNoRows) {
		return Cleared{}, nil
	}
	if err != nil {
		return Cleared{}, fmt.Errorf("settings: %w", err)
	}
	if hs.KeepHistoryMonths <= 0 {
		return Cleared{}, nil
	}
	cutoff := HistoryCutoff(s.now(), s.loc(ctx, householdID), int(hs.KeepHistoryMonths))
	at, day := cutoff.Unix(), cutoff.Format(DateLayout)

	var out Cleared
	err = s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		// Bank requests go first: their ledger lines can't be deleted while
		// a request points at them.
		if out.Transfers, err = q.DeleteOldTransferRequests(ctx, dbq.DeleteOldTransferRequestsParams{
			HouseholdID: householdID, CutoffUnix: &at,
		}); err != nil {
			return fmt.Errorf("bank requests: %w", err)
		}
		fold, err := q.ListLedgerToFold(ctx, dbq.ListLedgerToFoldParams{HouseholdID: householdID, CutoffUnix: at})
		if err != nil {
			return fmt.Errorf("ledger to fold: %w", err)
		}
		for _, f := range fold {
			if f.Lines == 1 && f.Folded == 1 {
				continue // already just a Brought forward line
			}
			// The oldest line becomes Brought forward, so it stays last in
			// My money's history. It's dated just before the cutoff, where
			// the lines it adds up were.
			if err := q.SetBroughtForward(ctx, dbq.SetBroughtForwardParams{
				Points: f.Points, Pence: f.Pence, CreatedAt: at - 1, HouseholdID: householdID, ID: f.KeepID,
			}); err != nil {
				return fmt.Errorf("brought forward: %w", err)
			}
			n, err := q.DeleteFoldedLedger(ctx, dbq.DeleteFoldedLedgerParams{
				HouseholdID: householdID, PersonID: f.PersonID, CutoffUnix: at, KeepID: f.KeepID,
			})
			if err != nil {
				return fmt.Errorf("fold ledger: %w", err)
			}
			out.Folded += n
		}
		if out.Redemptions, err = q.DeleteOldRedemptions(ctx, dbq.DeleteOldRedemptionsParams{
			HouseholdID: householdID, CutoffUnix: &at,
		}); err != nil {
			return fmt.Errorf("reward claims: %w", err)
		}
		if out.Completions, err = q.DeleteOldCompletions(ctx, dbq.DeleteOldCompletionsParams{
			HouseholdID: householdID, CutoffUnix: at,
		}); err != nil {
			return fmt.Errorf("completions: %w", err)
		}
		if out.Occurrences, err = q.DeleteOldOccurrences(ctx, dbq.DeleteOldOccurrencesParams{
			HouseholdID: householdID, CutoffDay: day,
		}); err != nil {
			return fmt.Errorf("chore copies: %w", err)
		}
		if out.Activity, err = q.DeleteOldActivity(ctx, dbq.DeleteOldActivityParams{
			HouseholdID: householdID, CutoffUnix: at,
		}); err != nil {
			return fmt.Errorf("activity: %w", err)
		}
		return nil
	})
	if err != nil {
		return Cleared{}, err
	}
	return out, nil
}
