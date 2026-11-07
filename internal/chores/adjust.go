package chores

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Manual adjustments: an adult gives someone a bonus, or takes points away,
// with a reason they'll see on the board. The balance can go below zero, and
// points held for waiting reward claims stay held.
const (
	// MaxAdjustment is the most points one adjustment can give or take.
	MaxAdjustment = 1000
	// MaxAdjustmentNote is the longest reason, in characters.
	MaxAdjustmentNote = 200
	// AdjustmentUndoWindow is how long an adjustment can be undone for.
	AdjustmentUndoWindow = 10 * time.Minute
)

// Adjustment is one manual adjustment, for the history lists.
type Adjustment struct {
	ID     int64
	Points int64 // positive for a bonus, negative for points taken away
	Note   string
	At     time.Time
	By     string // the adult who made it
}

// AdjustPoints gives personID points (or takes them away, when points is
// negative) with a reason. adultID must be an adult. Bad amounts or reasons
// come back as valid.Errors on "amount" and "reason".
func (s *Service) AdjustPoints(ctx context.Context, householdID, adultID, personID, points int64, note string) (dbq.LedgerEntry, error) {
	note = strings.TrimSpace(note)
	errs := valid.Errors{}
	if points == 0 || points > MaxAdjustment || points < -MaxAdjustment {
		errs.Add("amount", fmt.Sprintf("Choose how many points, from 1 to %d.", MaxAdjustment))
	}
	if n := len([]rune(note)); n < 1 || n > MaxAdjustmentNote {
		errs.Add("reason", fmt.Sprintf("Say what they did and why (up to %d characters).", MaxAdjustmentNote))
	}
	if err := errs.Err(); err != nil {
		return dbq.LedgerEntry{}, err
	}
	var out dbq.LedgerEntry
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		var err error
		out, err = q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
			HouseholdID: householdID,
			PersonID:    personID,
			Points:      points,
			Kind:        "manual",
			Note:        note,
			CreatedBy:   &adultID,
			CreatedAt:   s.now().Unix(),
		})
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		return s.activity(ctx, q, householdID, adultID, "points.adjusted", out.ID)
	})
	return out, err
}

// UndoAdjustment removes a manual adjustment made in the last
// AdjustmentUndoWindow. It returns ErrUndoExpired if it's older than that,
// already undone, or not a manual adjustment.
func (s *Service) UndoAdjustment(ctx context.Context, householdID, adultID, entryID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		n, err := q.DeleteManualEntry(ctx, dbq.DeleteManualEntryParams{
			HouseholdID: householdID, ID: entryID,
			SinceUnix: s.now().Add(-AdjustmentUndoWindow).Unix(),
		})
		if err != nil {
			return fmt.Errorf("undo adjustment: %w", err)
		}
		if n == 0 {
			return ErrUndoExpired
		}
		return s.activity(ctx, q, householdID, adultID, "points.undone", entryID)
	})
}

// Adjustments lists personID's manual adjustments since since, newest
// first, up to limit.
func (s *Service) Adjustments(ctx context.Context, householdID, personID int64, since time.Time, limit int) ([]Adjustment, error) {
	rows, err := s.st.Q.ListManualAdjustments(ctx, dbq.ListManualAdjustmentsParams{
		HouseholdID: householdID, PersonID: personID, SinceUnix: since.Unix(), Lim: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list adjustments: %w", err)
	}
	out := make([]Adjustment, len(rows))
	for i, r := range rows {
		out[i] = Adjustment{ID: r.ID, Points: r.Points, Note: r.Note, At: time.Unix(r.CreatedAt, 0), By: r.ByName}
	}
	return out, nil
}

// ReasonSuggestions returns the last few distinct reasons used, newest
// first, to offer on the form.
func (s *Service) ReasonSuggestions(ctx context.Context, householdID int64) ([]string, error) {
	notes, err := s.st.Q.RecentManualNotes(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("reason suggestions: %w", err)
	}
	return notes, nil
}
