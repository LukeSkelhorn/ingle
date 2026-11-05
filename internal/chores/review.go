package chores

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// ReverseWindow is how long an adult can take back an approved completion.
const ReverseWindow = 7 * 24 * time.Hour

var (
	// ErrTooOld is returned when reversing a completion older than
	// ReverseWindow.
	ErrTooOld = errors.New("too long ago to reverse")
	// ErrNotCounted is returned when reversing a completion that has already
	// been rejected or reversed.
	ErrNotCounted = errors.New("already not counted")
)

// CompletionDetail is a completion with what an adult needs to review it.
type CompletionDetail struct {
	ID           int64
	OccurrenceID int64
	Status       string
	CompletedAt  int64
	ReviewedAt   *int64
	Points       int64
	Pence        int64
	Person       dbq.Person // ID, Name, Colour and Avatar only
	ReviewerName string
	Title        string
	Emoji        string // the chore's, "" for none
	DueOn        string
	// CanReverse is set for approved completions inside ReverseWindow.
	CanReverse bool
}

// Pending reports whether the completion is waiting for an adult.
func (c CompletionDetail) Pending() bool { return c.Status == StatusPending }

// Counted reports whether the completion's points stand.
func (c CompletionDetail) Counted() bool { return c.Status == StatusApproved }

// Completions lists every pending completion plus everything completed in the
// last ReverseWindow, pending first, then newest first.
func (s *Service) Completions(ctx context.Context, householdID int64) ([]CompletionDetail, error) {
	now := s.now()
	rows, err := s.st.Q.ListCompletionDetails(ctx, dbq.ListCompletionDetailsParams{
		HouseholdID: householdID, SinceUnix: now.Add(-ReverseWindow).Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("list completions: %w", err)
	}
	out := make([]CompletionDetail, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.detail(now, dbq.GetCompletionDetailRow(r)))
	}
	return out, nil
}

// Completion returns one completion for review.
func (s *Service) Completion(ctx context.Context, householdID, completionID int64) (CompletionDetail, error) {
	r, err := s.st.Q.GetCompletionDetail(ctx, dbq.GetCompletionDetailParams{HouseholdID: householdID, ID: completionID})
	if err != nil {
		return CompletionDetail{}, notFound(err)
	}
	return s.detail(s.now(), r), nil
}

func (s *Service) detail(now time.Time, r dbq.GetCompletionDetailRow) CompletionDetail {
	d := CompletionDetail{
		ID:           r.ID,
		OccurrenceID: r.OccurrenceID,
		Status:       r.Status,
		CompletedAt:  r.CompletedAt,
		ReviewedAt:   r.ReviewedAt,
		Points:       r.Points,
		Pence:        r.Pence,
		Person:       dbq.Person{ID: r.PersonID, Name: r.PersonName, Colour: r.PersonColour, Avatar: r.PersonAvatar},
		Title:        r.Title,
		Emoji:        r.Emoji,
		DueOn:        r.DueOn,
	}
	if r.ReviewerName != nil {
		d.ReviewerName = *r.ReviewerName
	}
	d.CanReverse = r.Status == StatusApproved && withinReverseWindow(now, r.CompletedAt)
	return d
}

func withinReverseWindow(now time.Time, completedAt int64) bool {
	return now.Sub(time.Unix(completedAt, 0)) <= ReverseWindow
}

// Reverse is an adult taking back a completion that shouldn't have counted.
// A pending completion is simply rejected. An approved one, if completed
// within ReverseWindow, gets a compensating ledger adjustment (the original
// credit stays, so the ledger remains a true history) and is marked
// rejected. Either way the chore reopens on the board, claimed again if it
// was claimed. Balances may go negative if the points were already spent.
func (s *Service) Reverse(ctx context.Context, householdID, completionID, reviewerID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, reviewerID); err != nil {
			return err
		}
		c, err := q.GetCompletion(ctx, dbq.GetCompletionParams{HouseholdID: householdID, ID: completionID})
		if err != nil {
			return notFound(err)
		}
		switch c.Status {
		case StatusPending:
			return s.reviewTx(ctx, q, householdID, completionID, reviewerID, StatusRejected)
		case StatusRejected:
			return ErrNotCounted
		}
		now := s.now()
		if !withinReverseWindow(now, c.CompletedAt) {
			return ErrTooOld
		}

		ts := now.Unix()
		n, err := q.ReverseCompletion(ctx, dbq.ReverseCompletionParams{
			ReviewedBy: &reviewerID, ReviewedAt: &ts, HouseholdID: householdID, ID: c.ID,
		})
		if err != nil {
			return fmt.Errorf("reverse completion: %w", err)
		}
		if n == 0 {
			return ErrNotCounted
		}
		title := ""
		if occ, err := getOccurrence(ctx, q, householdID, c.OccurrenceID); err == nil {
			if ch, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: occ.ChoreID}); err == nil {
				title = ch.Title
			}
		}
		if c.Points != 0 || c.Pence != 0 {
			if _, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
				HouseholdID: householdID,
				PersonID:    c.PersonID,
				Points:      -c.Points,
				Pence:       -c.Pence,
				Kind:        "adjustment",
				Note:        "Reversed: " + title,
				CreatedBy:   &reviewerID,
				CreatedAt:   ts,
			}); err != nil {
				return fmt.Errorf("ledger: %w", err)
			}
		}
		if err := q.ReopenOccurrence(ctx, dbq.ReopenOccurrenceParams{HouseholdID: householdID, ID: c.OccurrenceID}); err != nil {
			return fmt.Errorf("reopen occurrence: %w", err)
		}
		return s.activity(ctx, q, householdID, reviewerID, "chore.reversed", c.ID)
	})
}
