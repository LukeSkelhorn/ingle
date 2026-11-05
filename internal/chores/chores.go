// Package chores holds the chore lifecycle on top of the generated queries:
// expanding recurrence rules into occurrences, claiming from the shared pool,
// completing, approving and redeeming rewards. Every state change that moves
// points or money also writes a ledger entry and an activity row in the same
// transaction.
package chores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// Chore modes.
const (
	ModeAssigned = "assigned"
	ModePool     = "pool"
)

// Status values shared by occurrences, completions and redemptions.
const (
	StatusOpen     = "open"
	StatusClaimed  = "claimed"
	StatusDone     = "done"
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// DateLayout is the storage format for local calendar dates.
const DateLayout = "2006-01-02"

// Errors returned by the lifecycle functions.
var (
	ErrNotFound            = errors.New("not found")
	ErrAlreadyClaimed      = errors.New("already claimed by someone else")
	ErrNotClaimable        = errors.New("chore is assigned, not in the shared pool")
	ErrNotClaimedByYou     = errors.New("not claimed by this person")
	ErrNotOpen             = errors.New("chore is already done")
	ErrNotYours            = errors.New("chore belongs to someone else")
	ErrNotPending          = errors.New("nothing pending to review")
	ErrNotAdult            = errors.New("only adults can do this")
	ErrInsufficientBalance = errors.New("not enough points or money")
	ErrInvalidRRule        = errors.New("invalid recurrence rule")
)

// Window is how many days ahead occurrences are materialised.
const Window = 14

// LocationFunc returns the timezone recurrence is evaluated in for a
// household. It is a func so a timezone change in settings applies without a
// restart.
type LocationFunc func(ctx context.Context, householdID int64) *time.Location

// FixedLocation returns a LocationFunc that always returns loc.
func FixedLocation(loc *time.Location) LocationFunc {
	return func(context.Context, int64) *time.Location { return loc }
}

// Service runs chore operations for the households in a store.
type Service struct {
	st  *store.Store
	loc LocationFunc
	now func() time.Time
}

// New returns a Service that evaluates recurrence in the zone loc returns.
func New(st *store.Store, loc LocationFunc) *Service {
	return &Service{st: st, loc: loc, now: time.Now}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ValidateRRule checks an RFC 5545 RRULE (without the "RRULE:" prefix).
func ValidateRRule(rule string) error {
	if _, err := rrule.StrToROption(rule); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRRule, err)
	}
	return nil
}

// Materialise creates occurrence rows for every active chore in the household
// for the local dates [from, from+days). It is idempotent and returns the
// number of new occurrences.
func (s *Service) Materialise(ctx context.Context, householdID int64, from time.Time, days int) (int, error) {
	loc := s.loc(ctx, householdID)
	start, end := window(from, days, loc)

	chores, err := s.st.Q.ListChores(ctx, householdID)
	if err != nil {
		return 0, fmt.Errorf("list chores: %w", err)
	}

	created := 0
	err = s.st.WithTx(ctx, func(q *dbq.Queries) error {
		for _, c := range chores {
			n, err := materialiseChore(ctx, q, c, start, end, loc)
			if err != nil {
				return err
			}
			created += n
		}
		return nil
	})
	return created, err
}

// window returns local midnight on from's date and days-1 days later.
func window(from time.Time, days int, loc *time.Location) (start, end time.Time) {
	y, m, d := from.In(loc).Date()
	start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	end = time.Date(y, m, d+days-1, 0, 0, 0, 0, loc)
	return start, end
}

func materialiseChore(ctx context.Context, q *dbq.Queries, c dbq.Chore, start, end time.Time, loc *time.Location) (int, error) {
	if c.DayMode == DayAny && c.Rrule != nil {
		return materialiseFlex(ctx, q, c, start, end, loc)
	}
	dates, err := occurrenceDates(c, start, end, loc)
	if err != nil {
		return 0, fmt.Errorf("chore %d: %w", c.ID, err)
	}
	created := 0
	for _, d := range dates {
		n, err := q.InsertOccurrence(ctx, dbq.InsertOccurrenceParams{
			HouseholdID: c.HouseholdID,
			ChoreID:     c.ID,
			DueOn:       d,
			AssigneeID:  c.AssigneeID,
		})
		if err != nil {
			return 0, fmt.Errorf("insert occurrence: %w", err)
		}
		created += int(n)
	}
	return created, nil
}

// materialiseFlex creates one occurrence per week for a flexible-day chore,
// on the board from the week's first allowed day and due on its last.
func materialiseFlex(ctx context.Context, q *dbq.Queries, c dbq.Chore, start, end time.Time, loc *time.Location) (int, error) {
	ws, err := flexWindows(c, start, end, loc)
	if err != nil {
		return 0, fmt.Errorf("chore %d: %w", c.ID, err)
	}
	created := 0
	for _, w := range ws {
		from := w.From()
		n, err := q.InsertOccurrence(ctx, dbq.InsertOccurrenceParams{
			HouseholdID:   c.HouseholdID,
			ChoreID:       c.ID,
			DueOn:         w.Due(),
			AssigneeID:    c.AssigneeID,
			AvailableFrom: &from,
		})
		if err != nil {
			return 0, fmt.Errorf("insert occurrence: %w", err)
		}
		created += int(n)
	}
	return created, nil
}

// occurrenceDates returns the local dates in [start, end] on which c is due.
// One-off chores (no rrule) are due on starts_on, and are still created when
// that date is already in the past so they don't silently disappear.
func occurrenceDates(c dbq.Chore, start, end time.Time, loc *time.Location) ([]string, error) {
	dtstart, err := time.ParseInLocation(DateLayout, c.StartsOn, loc)
	if err != nil {
		return nil, fmt.Errorf("starts_on: %w", err)
	}
	if c.Rrule == nil {
		if dtstart.After(end) {
			return nil, nil
		}
		return []string{c.StartsOn}, nil
	}

	opt, err := rrule.StrToROption(*c.Rrule)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRRule, err)
	}
	opt.Dtstart = dtstart
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRRule, err)
	}

	var dates []string
	for _, t := range r.Between(start, end, true) {
		dates = append(dates, t.In(loc).Format(DateLayout))
	}
	return dates, nil
}

// Claim locks a pool occurrence to personID.
func (s *Service) Claim(ctx context.Context, householdID, occurrenceID, personID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		n, err := q.ClaimOccurrence(ctx, dbq.ClaimOccurrenceParams{
			PersonID:    &personID,
			ClaimedAt:   ptr(s.now().Unix()),
			HouseholdID: householdID,
			ID:          occurrenceID,
		})
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		if n == 0 {
			occ, err := getOccurrence(ctx, q, householdID, occurrenceID)
			if err != nil {
				return err
			}
			switch {
			case occ.AssigneeID != nil:
				return ErrNotClaimable
			case occ.Status == StatusDone:
				return ErrNotOpen
			default:
				return ErrAlreadyClaimed
			}
		}
		return s.activity(ctx, q, householdID, personID, "chore.claimed", occurrenceID)
	})
}

// Release returns a claimed occurrence to the pool.
func (s *Service) Release(ctx context.Context, householdID, occurrenceID, personID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		n, err := q.ReleaseOccurrence(ctx, dbq.ReleaseOccurrenceParams{
			HouseholdID: householdID,
			ID:          occurrenceID,
			PersonID:    &personID,
		})
		if err != nil {
			return fmt.Errorf("release: %w", err)
		}
		if n == 0 {
			if _, err := getOccurrence(ctx, q, householdID, occurrenceID); err != nil {
				return err
			}
			return ErrNotClaimedByYou
		}
		return s.activity(ctx, q, householdID, personID, "chore.released", occurrenceID)
	})
}

// Complete records personID finishing an occurrence. Assigned chores can only
// be completed by their assignee, and claimed pool chores by the claimant; an
// unclaimed pool chore can be completed by anyone. Chores that require
// approval stay pending until an adult approves them; others are credited
// straight away.
func (s *Service) Complete(ctx context.Context, householdID, occurrenceID, personID int64) (dbq.Completion, error) {
	var out dbq.Completion
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		occ, err := getOccurrence(ctx, q, householdID, occurrenceID)
		if err != nil {
			return err
		}
		if occ.Status == StatusDone {
			return ErrNotOpen
		}
		owner := occ.AssigneeID
		if owner == nil {
			owner = occ.ClaimedBy
		}
		if owner != nil && *owner != personID {
			return ErrNotYours
		}
		chore, err := q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: householdID, ID: occ.ChoreID})
		if err != nil {
			return fmt.Errorf("get chore: %w", err)
		}

		status := StatusApproved
		if chore.RequiresApproval == 1 {
			status = StatusPending
		}
		now := s.now().Unix()
		out, err = q.CreateCompletion(ctx, dbq.CreateCompletionParams{
			HouseholdID:  householdID,
			OccurrenceID: occurrenceID,
			PersonID:     personID,
			CompletedAt:  now,
			Status:       status,
			Points:       chore.Points,
			Pence:        chore.Pence,
		})
		if err != nil {
			return fmt.Errorf("create completion: %w", err)
		}
		if err := q.SetOccurrenceStatus(ctx, dbq.SetOccurrenceStatusParams{
			Status: StatusDone, HouseholdID: householdID, ID: occurrenceID,
		}); err != nil {
			return fmt.Errorf("mark done: %w", err)
		}
		if status == StatusApproved {
			if err := creditCompletion(ctx, q, out, nil, now); err != nil {
				return err
			}
		}
		return s.activity(ctx, q, householdID, personID, "chore.completed", out.ID)
	})
	return out, err
}

// Approve credits a pending completion. reviewerID must be an adult.
func (s *Service) Approve(ctx context.Context, householdID, completionID, reviewerID int64) error {
	return s.review(ctx, householdID, completionID, reviewerID, StatusApproved)
}

// Reject declines a pending completion and reopens the occurrence so it can
// be done again. reviewerID must be an adult.
func (s *Service) Reject(ctx context.Context, householdID, completionID, reviewerID int64) error {
	return s.review(ctx, householdID, completionID, reviewerID, StatusRejected)
}

func (s *Service) review(ctx context.Context, householdID, completionID, reviewerID int64, status string) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, reviewerID); err != nil {
			return err
		}
		return s.reviewTx(ctx, q, householdID, completionID, reviewerID, status)
	})
}

func requireAdult(ctx context.Context, q *dbq.Queries, householdID, personID int64) error {
	p, err := getPerson(ctx, q, householdID, personID)
	if err != nil {
		return err
	}
	if p.Kind != "adult" {
		return ErrNotAdult
	}
	return nil
}

func (s *Service) reviewTx(ctx context.Context, q *dbq.Queries, householdID, completionID, reviewerID int64, status string) error {
	now := s.now().Unix()
	n, err := q.ReviewCompletion(ctx, dbq.ReviewCompletionParams{
		Status:      status,
		ReviewedBy:  &reviewerID,
		ReviewedAt:  &now,
		HouseholdID: householdID,
		ID:          completionID,
	})
	if err != nil {
		return fmt.Errorf("review completion: %w", err)
	}
	if n == 0 {
		if _, err := q.GetCompletion(ctx, dbq.GetCompletionParams{HouseholdID: householdID, ID: completionID}); err != nil {
			return notFound(err)
		}
		return ErrNotPending
	}
	c, err := q.GetCompletion(ctx, dbq.GetCompletionParams{HouseholdID: householdID, ID: completionID})
	if err != nil {
		return fmt.Errorf("get completion: %w", err)
	}

	verb := "chore.approved"
	if status == StatusApproved {
		if err := creditCompletion(ctx, q, c, &reviewerID, now); err != nil {
			return err
		}
	} else {
		verb = "chore.rejected"
		if err := q.ReopenOccurrence(ctx, dbq.ReopenOccurrenceParams{HouseholdID: householdID, ID: c.OccurrenceID}); err != nil {
			return fmt.Errorf("reopen occurrence: %w", err)
		}
	}
	return s.activity(ctx, q, householdID, reviewerID, verb, completionID)
}

func creditCompletion(ctx context.Context, q *dbq.Queries, c dbq.Completion, by *int64, now int64) error {
	if _, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID:  c.HouseholdID,
		PersonID:     c.PersonID,
		Points:       c.Points,
		Pence:        c.Pence,
		Kind:         "completion",
		CompletionID: &c.ID,
		CreatedBy:    by,
		CreatedAt:    now,
	}); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

func (s *Service) activity(ctx context.Context, q *dbq.Queries, householdID, actorID int64, verb string, subjectID int64) error {
	subjectType := map[string]string{
		"chore.claimed":    "occurrence",
		"chore.released":   "occurrence",
		"chore.completed":  "completion",
		"chore.undone":     "occurrence",
		"chore.approved":   "completion",
		"chore.rejected":   "completion",
		"chore.reversed":   "completion",
		"reward.requested": "redemption",
		"reward.redeemed":  "redemption",
		"reward.declined":  "redemption",
		"reward.cancelled": "redemption",
	}[verb]
	if err := q.InsertActivity(ctx, dbq.InsertActivityParams{
		HouseholdID: householdID,
		ActorID:     &actorID,
		Verb:        verb,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		CreatedAt:   s.now().Unix(),
	}); err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	return nil
}

// getPerson loads an active person, scoped to the household so IDs from
// another household can't be used.
func getPerson(ctx context.Context, q *dbq.Queries, householdID, id int64) (dbq.Person, error) {
	p, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id})
	if err != nil {
		return dbq.Person{}, notFound(err)
	}
	if p.ArchivedAt != nil {
		return dbq.Person{}, ErrNotFound
	}
	return p, nil
}

func getOccurrence(ctx context.Context, q *dbq.Queries, householdID, id int64) (dbq.ChoreOccurrence, error) {
	o, err := q.GetOccurrence(ctx, dbq.GetOccurrenceParams{HouseholdID: householdID, ID: id})
	if err != nil {
		return dbq.ChoreOccurrence{}, notFound(err)
	}
	return o, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func ptr[T any](v T) *T { return &v }
