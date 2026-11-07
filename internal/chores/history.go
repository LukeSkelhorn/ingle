package chores

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// HistoryPage is how many older entries Show more adds to My money's history.
const HistoryPage = 30

// historyMax bounds the first page of history, however busy the last 30
// days were.
const historyMax = 200

// HistoryItem is one line of someone's money history: a ledger entry, or a
// reward claim still waiting for a grown-up.
type HistoryItem struct {
	ID int64
	// Kind is the ledger kind (completion, adjustment, redemption, payout,
	// manual), or "waiting" for a claim that hasn't been decided yet.
	Kind          string
	Title, Emoji  string // the chore or reward, if any
	Note          string // an adult's reason, or what a reversal undid
	Points, Pence int64  // signed: what it added or took away
	At            int64  // unix seconds
}

// History is a person's money history, newest first: every entry since
// since, then, with Show more, HistoryPage entries at a time older than
// before (0 for the first page). next is what to pass as before for the
// next page, or 0 when there's nothing older.
func (s *Service) History(ctx context.Context, householdID, personID int64, since time.Time, before int64) (items []HistoryItem, next int64, err error) {
	q := s.st.Q
	if _, err := getPerson(ctx, q, householdID, personID); err != nil {
		return nil, 0, err
	}
	arg := dbq.ListLedgerHistoryParams{HouseholdID: householdID, PersonID: personID, BeforeID: before, Lim: HistoryPage + 1}
	if before <= 0 {
		arg.BeforeID, arg.SinceUnix, arg.Lim = math.MaxInt64, since.Unix(), historyMax+1
	}
	rows, err := q.ListLedgerHistory(ctx, arg)
	if err != nil {
		return nil, 0, fmt.Errorf("history: %w", err)
	}
	if n := int(arg.Lim) - 1; len(rows) > n {
		rows = rows[:n]
		next = rows[n-1].ID
	}
	items = make([]HistoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, HistoryItem{
			ID: r.ID, Kind: r.Kind, Title: r.Title, Emoji: r.Emoji, Note: r.Note,
			Points: r.Points, Pence: r.Pence, At: r.CreatedAt,
		})
	}
	if next == 0 && arg.SinceUnix > 0 {
		// The first page stops at since; is there anything before it?
		from := int64(math.MaxInt64)
		if len(items) > 0 {
			from = items[len(items)-1].ID
		}
		older, err := q.ListLedgerHistory(ctx, dbq.ListLedgerHistoryParams{
			HouseholdID: householdID, PersonID: personID, BeforeID: from, Lim: 1,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("older history: %w", err)
		}
		if len(older) > 0 {
			next = from
		}
	}
	return items, next, nil
}

// WaitingClaims are a person's reward claims still waiting for a grown-up,
// newest first, as history lines that take away what they hold.
func (s *Service) WaitingClaims(ctx context.Context, householdID, personID int64) ([]HistoryItem, error) {
	rows, err := s.st.Q.ListPendingRedemptionsForPerson(ctx, dbq.ListPendingRedemptionsForPersonParams{
		HouseholdID: householdID, PersonID: personID,
	})
	if err != nil {
		return nil, fmt.Errorf("waiting claims: %w", err)
	}
	items := make([]HistoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, HistoryItem{
			ID: r.ID, Kind: "waiting", Title: r.Title, Emoji: r.Emoji,
			Points: -r.Points, Pence: -r.Pence, At: r.RequestedAt,
		})
	}
	return items, nil
}
