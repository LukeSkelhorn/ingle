package chores

import (
	"context"
	"fmt"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// Rewards are claimed from the board and wait for an adult. A waiting claim
// holds its cost: nothing is written to the ledger until an adult approves,
// but what a person can spend is their balance less what their waiting
// claims hold. Declining or cancelling a claim simply lets the hold go, so
// the ledger only ever records real spending.

// RedemptionDetail is a reward claim with who and what, for review.
type RedemptionDetail struct {
	ID          int64
	RewardID    int64
	Status      string
	RequestedAt int64
	Points      int64
	Pence       int64
	Person      dbq.Person // ID, Name, Colour and Avatar only
	Title       string     // the reward
}

// Pending reports whether the claim is waiting for an adult.
func (d RedemptionDetail) Pending() bool { return d.Status == StatusPending }

// ClaimReward asks for a reward on personID's behalf. It fails with
// ErrInsufficientBalance unless what they can spend (their balance less
// their waiting claims) covers it. The claim waits for an adult, unless
// approver (an adult who is there) is given, when it's approved at once.
func (s *Service) ClaimReward(ctx context.Context, householdID, rewardID, personID int64, approver *int64) (dbq.Redemption, error) {
	var out dbq.Redemption
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		if approver != nil {
			if err := requireAdult(ctx, q, householdID, *approver); err != nil {
				return err
			}
		}
		reward, err := q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: rewardID})
		if err != nil {
			return notFound(err)
		}
		if reward.ArchivedAt != nil {
			return ErrNotFound
		}
		points, pence, err := available(ctx, q, householdID, personID)
		if err != nil {
			return err
		}
		if points < reward.CostPoints || pence < reward.CostPence {
			return ErrInsufficientBalance
		}

		out, err = q.CreateRedemption(ctx, dbq.CreateRedemptionParams{
			HouseholdID: householdID,
			RewardID:    rewardID,
			PersonID:    personID,
			Points:      reward.CostPoints,
			Pence:       reward.CostPence,
			Status:      StatusPending,
			RequestedAt: s.now().Unix(),
		})
		if err != nil {
			return fmt.Errorf("create redemption: %w", err)
		}
		if err := s.activity(ctx, q, householdID, personID, "reward.requested", out.ID); err != nil {
			return err
		}
		if approver != nil {
			if err := s.approveTx(ctx, q, householdID, out.ID, *approver); err != nil {
				return err
			}
			out.Status = StatusApproved
		}
		return nil
	})
	return out, err
}

// available is what personID can spend: their balance less waiting claims.
func available(ctx context.Context, q *dbq.Queries, householdID, personID int64) (points, pence int64, err error) {
	bal, err := q.BalanceForPerson(ctx, dbq.BalanceForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return 0, 0, fmt.Errorf("balance: %w", err)
	}
	held, err := q.HeldForPerson(ctx, dbq.HeldForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return 0, 0, fmt.Errorf("held: %w", err)
	}
	return bal.Points - held.Points, bal.Pence - held.Pence, nil
}

// ApproveRedemption spends a waiting claim: the hold becomes a ledger
// entry. reviewerID must be an adult.
func (s *Service) ApproveRedemption(ctx context.Context, householdID, redemptionID, reviewerID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, reviewerID); err != nil {
			return err
		}
		return s.approveTx(ctx, q, householdID, redemptionID, reviewerID)
	})
}

func (s *Service) approveTx(ctx context.Context, q *dbq.Queries, householdID, redemptionID, reviewerID int64) error {
	d, err := s.settle(ctx, q, householdID, redemptionID, reviewerID, StatusApproved)
	if err != nil {
		return err
	}
	reward, err := q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: d.RewardID})
	if err != nil {
		return fmt.Errorf("get reward: %w", err)
	}
	if _, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID:  householdID,
		PersonID:     d.PersonID,
		Points:       -d.Points,
		Pence:        -d.Pence,
		Kind:         "redemption",
		RedemptionID: &d.ID,
		Note:         reward.Title,
		CreatedBy:    &reviewerID,
		CreatedAt:    s.now().Unix(),
	}); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	// The child is the actor, so the feed reads "Alfie claimed Lego set".
	return s.activity(ctx, q, householdID, d.PersonID, "reward.redeemed", d.ID)
}

// DeclineRedemption turns down a waiting claim and lets its hold go.
// reviewerID must be an adult.
func (s *Service) DeclineRedemption(ctx context.Context, householdID, redemptionID, reviewerID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, reviewerID); err != nil {
			return err
		}
		if _, err := s.settle(ctx, q, householdID, redemptionID, reviewerID, StatusRejected); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, reviewerID, "reward.declined", redemptionID)
	})
}

// CancelRedemption is the claimant changing their mind about a waiting
// claim. It's stored as rejected, reviewed by the claimant themselves.
func (s *Service) CancelRedemption(ctx context.Context, householdID, redemptionID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		d, err := q.GetRedemption(ctx, dbq.GetRedemptionParams{HouseholdID: householdID, ID: redemptionID})
		if err != nil {
			return notFound(err)
		}
		if _, err := s.settle(ctx, q, householdID, redemptionID, d.PersonID, StatusRejected); err != nil {
			return err
		}
		return s.activity(ctx, q, householdID, d.PersonID, "reward.cancelled", redemptionID)
	})
}

// settle moves a pending claim to status, failing with ErrNotPending if
// someone got there first.
func (s *Service) settle(ctx context.Context, q *dbq.Queries, householdID, redemptionID, by int64, status string) (dbq.Redemption, error) {
	now := s.now().Unix()
	n, err := q.ReviewRedemption(ctx, dbq.ReviewRedemptionParams{
		Status: status, ReviewedBy: &by, ReviewedAt: &now, HouseholdID: householdID, ID: redemptionID,
	})
	if err != nil {
		return dbq.Redemption{}, fmt.Errorf("review redemption: %w", err)
	}
	d, err := q.GetRedemption(ctx, dbq.GetRedemptionParams{HouseholdID: householdID, ID: redemptionID})
	if err != nil {
		return dbq.Redemption{}, notFound(err)
	}
	if n == 0 {
		return dbq.Redemption{}, ErrNotPending
	}
	return d, nil
}

// PendingRedemptions lists waiting claims, oldest first: everyone's, or
// just personID's.
func (s *Service) PendingRedemptions(ctx context.Context, householdID int64, personID *int64) ([]RedemptionDetail, error) {
	rows, err := s.st.Q.ListPendingRedemptionDetails(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	out := make([]RedemptionDetail, 0, len(rows))
	for _, r := range rows {
		if personID == nil || r.PersonID == *personID {
			out = append(out, redemptionDetail(dbq.GetRedemptionDetailRow(r)))
		}
	}
	return out, nil
}

// Redemption returns one claim for review.
func (s *Service) Redemption(ctx context.Context, householdID, redemptionID int64) (RedemptionDetail, error) {
	r, err := s.st.Q.GetRedemptionDetail(ctx, dbq.GetRedemptionDetailParams{HouseholdID: householdID, ID: redemptionID})
	if err != nil {
		return RedemptionDetail{}, notFound(err)
	}
	return redemptionDetail(r), nil
}

func redemptionDetail(r dbq.GetRedemptionDetailRow) RedemptionDetail {
	return RedemptionDetail{
		ID:          r.ID,
		RewardID:    r.RewardID,
		Status:      r.Status,
		RequestedAt: r.RequestedAt,
		Points:      r.Points,
		Pence:       r.Pence,
		Person:      dbq.Person{ID: r.PersonID, Name: r.PersonName, Colour: r.PersonColour, Avatar: r.PersonAvatar},
		Title:       r.Title,
	}
}
