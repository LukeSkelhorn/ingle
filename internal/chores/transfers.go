package chores

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Bank transfers: someone asks for some of their money to go to their real
// bank. Ingle never moves real money: an adult does the transfer in their
// banking app, then approves the request here, which writes a payout to the
// ledger. A waiting request holds its pence, as a waiting reward claim does,
// so the same money can't be spent on a reward too. An adult can also pay
// out directly, which is stored as an already-approved request.

// Transfer request statuses beyond StatusPending and StatusApproved.
const (
	StatusDeclined  = "declined"
	StatusCancelled = "cancelled"
)

// MaxTransferReason is the longest reason for declining, in characters.
const MaxTransferReason = 200

// TransferNote is the ledger note on a payout.
const TransferNote = "Moved to bank"

// ErrTransferPending is returned when they already have a request waiting.
var ErrTransferPending = errors.New("a bank request is already waiting")

// Transfer is a bank transfer request with who it's for and who decided.
type Transfer struct {
	ID          int64
	Person      dbq.Person // ID, Name, Colour and Avatar only
	Pence       int64
	Status      string
	Reason      string // why it was declined
	RequestedAt int64
	ReviewedAt  int64  // 0 while waiting
	RequestedBy string // who asked: them, or the adult who paid out
	ReviewedBy  string
}

// Pending reports whether the request is waiting for an adult.
func (t Transfer) Pending() bool { return t.Status == StatusPending }

// RequestTransfer asks for pence of personID's money to go to their bank.
// It fails with ErrInsufficientBalance unless what they can spend covers
// it, and with ErrTransferPending if they already have one waiting.
func (s *Service) RequestTransfer(ctx context.Context, householdID, personID, pence int64) (dbq.TransferRequest, error) {
	var out dbq.TransferRequest
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		waiting, err := q.PendingTransfersForPerson(ctx, dbq.PendingTransfersForPersonParams{HouseholdID: householdID, PersonID: personID})
		if err != nil {
			return fmt.Errorf("pending transfers: %w", err)
		}
		if len(waiting) > 0 {
			return ErrTransferPending
		}
		if err := canSpend(ctx, q, householdID, personID, pence); err != nil {
			return err
		}
		out, err = q.CreateTransferRequest(ctx, dbq.CreateTransferRequestParams{
			HouseholdID: householdID, PersonID: personID, Pence: pence, Status: StatusPending,
			RequestedBy: &personID, RequestedAt: s.now().Unix(),
		})
		if err != nil {
			return fmt.Errorf("create transfer: %w", err)
		}
		return s.activity(ctx, q, householdID, personID, "transfer.requested", out.ID)
	})
	return out, err
}

// canSpend checks pence is a real amount that personID can spend.
func canSpend(ctx context.Context, q *dbq.Queries, householdID, personID, pence int64) error {
	if pence <= 0 {
		return ErrInsufficientBalance
	}
	_, have, err := available(ctx, q, householdID, personID)
	if err != nil {
		return err
	}
	if pence > have {
		return ErrInsufficientBalance
	}
	return nil
}

// CancelTransfer is personID changing their mind about their waiting
// request.
func (s *Service) CancelTransfer(ctx context.Context, householdID, personID, transferID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		t, err := q.GetTransferRequest(ctx, dbq.GetTransferRequestParams{HouseholdID: householdID, ID: transferID})
		if err != nil {
			return notFound(err)
		}
		if t.PersonID != personID {
			return ErrNotFound
		}
		if err := s.settleTransfer(ctx, q, householdID, transferID, personID, StatusCancelled, ""); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, personID, "transfer.cancelled", transferID)
	})
}

// ApproveTransfer is an adult saying "I've sent it": the request becomes a
// payout in the ledger.
func (s *Service) ApproveTransfer(ctx context.Context, householdID, transferID, adultID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if err := s.settleTransfer(ctx, q, householdID, transferID, adultID, StatusApproved, ""); err != nil {
			return err
		}
		t, err := q.GetTransferRequest(ctx, dbq.GetTransferRequestParams{HouseholdID: householdID, ID: transferID})
		if err != nil {
			return notFound(err)
		}
		if err := s.payout(ctx, q, t, adultID); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, adultID, "transfer.approved", transferID)
	})
}

// DeclineTransfer turns down a waiting request with a reason they'll see,
// and lets its hold go. Without a reason it returns valid.Errors on
// "reason".
func (s *Service) DeclineTransfer(ctx context.Context, householdID, transferID, adultID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if n := len([]rune(reason)); n < 1 || n > MaxTransferReason {
		errs := valid.Errors{}
		errs.Add("reason", fmt.Sprintf("Say why, in a few words (up to %d characters).", MaxTransferReason))
		return errs.Err()
	}
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if err := s.settleTransfer(ctx, q, householdID, transferID, adultID, StatusDeclined, reason); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, adultID, "transfer.declined", transferID)
	})
}

// PayOut records an adult moving pence of personID's money to their bank
// themselves: an approved request and its payout, at once.
func (s *Service) PayOut(ctx context.Context, householdID, personID, pence, adultID int64) (dbq.TransferRequest, error) {
	var out dbq.TransferRequest
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		if err := canSpend(ctx, q, householdID, personID, pence); err != nil {
			return err
		}
		now := s.now().Unix()
		var err error
		out, err = q.CreateTransferRequest(ctx, dbq.CreateTransferRequestParams{
			HouseholdID: householdID, PersonID: personID, Pence: pence, Status: StatusApproved,
			RequestedBy: &adultID, RequestedAt: now, ReviewedBy: &adultID, ReviewedAt: &now,
		})
		if err != nil {
			return fmt.Errorf("create transfer: %w", err)
		}
		if err := s.payout(ctx, q, out, adultID); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, adultID, "transfer.paid", out.ID)
	})
	return out, err
}

// payout writes t's payout to the ledger and links it.
func (s *Service) payout(ctx context.Context, q *dbq.Queries, t dbq.TransferRequest, adultID int64) error {
	e, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID: t.HouseholdID,
		PersonID:    t.PersonID,
		Pence:       -t.Pence,
		Kind:        "payout",
		Note:        TransferNote,
		CreatedBy:   &adultID,
		CreatedAt:   s.now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := q.SetTransferLedgerEntry(ctx, dbq.SetTransferLedgerEntryParams{
		LedgerEntryID: &e.ID, HouseholdID: t.HouseholdID, ID: t.ID,
	}); err != nil {
		return fmt.Errorf("link payout: %w", err)
	}
	return nil
}

// settleTransfer moves a waiting request to status, failing with
// ErrNotPending if someone got there first.
func (s *Service) settleTransfer(ctx context.Context, q *dbq.Queries, householdID, transferID, by int64, status, reason string) error {
	now := s.now().Unix()
	n, err := q.SettleTransferRequest(ctx, dbq.SettleTransferRequestParams{
		Status: status, Reason: reason, ReviewedBy: &by, ReviewedAt: &now, HouseholdID: householdID, ID: transferID,
	})
	if err != nil {
		return fmt.Errorf("settle transfer: %w", err)
	}
	if n == 0 {
		if _, err := q.GetTransferRequest(ctx, dbq.GetTransferRequestParams{HouseholdID: householdID, ID: transferID}); err != nil {
			return notFound(err)
		}
		return ErrNotPending
	}
	return nil
}

// Transfers lists every waiting request and the rest made since since,
// newest first.
func (s *Service) Transfers(ctx context.Context, householdID int64, since time.Time) ([]Transfer, error) {
	rows, err := s.st.Q.ListTransferDetails(ctx, dbq.ListTransferDetailsParams{HouseholdID: householdID, SinceUnix: since.Unix()})
	if err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	out := make([]Transfer, 0, len(rows))
	for _, r := range rows {
		t := Transfer{
			ID:          r.ID,
			Person:      dbq.Person{ID: r.PersonID, Name: r.PersonName, Colour: r.PersonColour, Avatar: r.PersonAvatar},
			Pence:       r.Pence,
			Status:      r.Status,
			Reason:      r.Reason,
			RequestedAt: r.RequestedAt,
			RequestedBy: r.RequestedByName,
			ReviewedBy:  r.ReviewedByName,
		}
		if r.ReviewedAt != nil {
			t.ReviewedAt = *r.ReviewedAt
		}
		out = append(out, t)
	}
	return out, nil
}

// PendingTransferCount is how many requests are waiting, for Admin home.
func (s *Service) PendingTransferCount(ctx context.Context, householdID int64) (int, error) {
	n, err := s.st.Q.CountPendingTransfers(ctx, householdID)
	if err != nil {
		return 0, fmt.Errorf("count transfers: %w", err)
	}
	return int(n), nil
}

// BankStatus is what My money shows about someone's bank requests: the one
// waiting, if any, and their latest since since if it was declined, so they
// see why.
type BankStatus struct {
	Waiting  *dbq.TransferRequest
	Declined *dbq.TransferRequest
}

// BankStatus returns personID's waiting and recently declined requests.
func (s *Service) BankStatus(ctx context.Context, householdID, personID int64, since time.Time) (BankStatus, error) {
	var b BankStatus
	waiting, err := s.st.Q.PendingTransfersForPerson(ctx, dbq.PendingTransfersForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return b, fmt.Errorf("pending transfers: %w", err)
	}
	if len(waiting) > 0 {
		b.Waiting = &waiting[0]
	}
	last, err := s.st.Q.LastSettledTransferForPerson(ctx, dbq.LastSettledTransferForPersonParams{
		HouseholdID: householdID, PersonID: personID, SinceUnix: since.Unix(),
	})
	if err != nil {
		return b, fmt.Errorf("declined transfers: %w", err)
	}
	if len(last) > 0 && last[0].Status == StatusDeclined {
		b.Declined = &last[0]
	}
	return b, nil
}
