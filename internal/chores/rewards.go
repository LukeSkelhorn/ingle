package chores

import (
	"context"
	"fmt"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
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
	Emoji       string     // the reward's, "" for none
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
		if err := rewardAllowed(ctx, q, reward, personID); err != nil {
			return err
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

// rewardAllowed fails with ErrNotFound unless reward is active and personID
// may have it: it's for everyone or chosen for them, and it costs no money
// if they're an adult.
func rewardAllowed(ctx context.Context, q *dbq.Queries, reward dbq.Reward, personID int64) error {
	if reward.ArchivedAt != nil {
		return ErrNotFound
	}
	ok, err := q.RewardAllowed(ctx, dbq.RewardAllowedParams{HouseholdID: reward.HouseholdID, RewardID: reward.ID, PersonID: personID})
	if err != nil {
		return fmt.Errorf("reward allowed: %w", err)
	}
	if ok == 0 {
		return ErrNotFound
	}
	return nil
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
	// Saving for it is done.
	if err := q.ClearGoalIf(ctx, dbq.ClearGoalIfParams{HouseholdID: householdID, ID: d.PersonID, RewardID: &d.RewardID}); err != nil {
		return fmt.Errorf("clear goal: %w", err)
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
		Emoji:       r.Emoji,
	}
}

// DefaultGoalEmoji stands in for a reward without an emoji.
const DefaultGoalEmoji = "🎯"

// Goal is the reward a person is saving for and how close they are, from
// what they can spend (their balance less waiting claims).
type Goal struct {
	RewardID              int64
	Title                 string
	RewardEmoji           string
	CostPoints, CostPence int64
	SetAt                 int64 // when they picked it
	HavePoints, HavePence int64 // towards the cost: 0 to the cost
	Percent               int   // the slower of points and money, 0 to 100
	Reached               bool  // they can claim it now
	Waiting               bool  // they've asked for it; a grown-up decides
}

// Emoji is the reward's emoji, or DefaultGoalEmoji.
func (g Goal) Emoji() string {
	if g.RewardEmoji == "" {
		return DefaultGoalEmoji
	}
	return g.RewardEmoji
}

// NeedPoints is how many more points they need for it.
func (g Goal) NeedPoints() int64 { return g.CostPoints - g.HavePoints }

// NeedPence is how much more money they need for it.
func (g Goal) NeedPence() int64 { return g.CostPence - g.HavePence }

// newGoal works out progress towards row from what the person can spend.
// For a reward that costs points and money, progress is the slower of the
// two, so the bar only fills once both are covered. While a claim for it is
// waiting the hold has already taken the cost, so it shows as full.
func newGoal(row dbq.ListGoalsRow, points, pence int64) Goal {
	g := Goal{
		RewardID: row.ID, Title: row.Title, RewardEmoji: row.Emoji,
		CostPoints: row.CostPoints, CostPence: row.CostPence, SetAt: row.SetAt,
		Waiting: row.Waiting != 0,
	}
	if g.Waiting {
		points, pence = g.CostPoints, g.CostPence
	}
	g.HavePoints = min(max(points, 0), g.CostPoints)
	g.HavePence = min(max(pence, 0), g.CostPence)
	g.Percent = 100
	if g.CostPoints > 0 {
		g.Percent = min(g.Percent, int(g.HavePoints*100/g.CostPoints))
	}
	if g.CostPence > 0 {
		g.Percent = min(g.Percent, int(g.HavePence*100/g.CostPence))
	}
	g.Reached = !g.Waiting && g.Percent == 100
	return g
}

// SetGoal has personID save for rewardID, replacing any goal they had.
// Anyone at the board can do this. It returns ErrNotFound for an archived
// person or reward, or one from another household.
func (s *Service) SetGoal(ctx context.Context, householdID, personID, rewardID int64) (dbq.Reward, error) {
	var reward dbq.Reward
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		var err error
		reward, err = q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: rewardID})
		if err != nil {
			return notFound(err)
		}
		if err := rewardAllowed(ctx, q, reward, personID); err != nil {
			return err
		}
		now := s.now().Unix()
		n, err := q.SetGoal(ctx, dbq.SetGoalParams{RewardID: &rewardID, SetAt: &now, HouseholdID: householdID, ID: personID})
		if err != nil {
			return fmt.Errorf("set goal: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return reward, err
}

// ClearGoal stops personID saving for anything.
func (s *Service) ClearGoal(ctx context.Context, householdID, personID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := getPerson(ctx, q, householdID, personID); err != nil {
			return err
		}
		if err := q.ClearGoal(ctx, dbq.ClearGoalParams{HouseholdID: householdID, ID: personID}); err != nil {
			return fmt.Errorf("clear goal: %w", err)
		}
		return nil
	})
}
