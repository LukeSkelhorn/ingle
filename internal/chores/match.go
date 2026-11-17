package chores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

const (
	moduleRewards = "rewards"
	moduleMoney   = "money"
)

// ModulesFunc says whether a household has a module on, reading through q.
type ModulesFunc func(ctx context.Context, q *dbq.Queries, householdID int64, id string) bool

// SetModules sets where module state comes from (every module on until set).
func (s *Service) SetModules(f ModulesFunc) { s.modules = f }

func (s *Service) moduleOn(ctx context.Context, q *dbq.Queries, householdID int64, id string) bool {
	return s.modules == nil || s.modules(ctx, q, householdID, id)
}

const (
	matchTakenBackGoal   = "Match taken back: goal changed"
	matchTakenBackChore  = "Match taken back: "
	matchNotePrefix      = "Matched by "
	matchTakenBackPrefix = "Match taken back"
)

// MatchOffer is a child's match offer on what they earn from chores.
type MatchOffer struct {
	AddPence, PerPence    int64
	AddPoints, PerPoints  int64
	CapPence, CapPoints   int64
	SetBy                 int64
	SetByName             string
	HeldPence, HeldPoints int64
}

// Money says whether money is matched.
func (o MatchOffer) Money() bool { return o.AddPence > 0 && o.PerPence > 0 }

// Points says whether points are matched.
func (o MatchOffer) Points() bool { return o.AddPoints > 0 && o.PerPoints > 0 }

// LeftPence is how much more money the cap allows, or -1 for no limit.
func (o MatchOffer) LeftPence() int64 { return capLeft(o.CapPence, o.HeldPence) }

// LeftPoints is how many more points the cap allows, or -1 for no limit.
func (o MatchOffer) LeftPoints() int64 { return capLeft(o.CapPoints, o.HeldPoints) }

// Capped says whether a cap limits what's matched (in a half that's on).
func (o MatchOffer) Capped() bool {
	return (o.Money() && o.CapPence > 0) || (o.Points() && o.CapPoints > 0)
}

// CapLeftPoints is how many more points the cap allows (0 if not matched and capped).
func (o MatchOffer) CapLeftPoints() int64 {
	if !o.Points() {
		return 0
	}
	return max(o.LeftPoints(), 0)
}

// CapLeftPence is how much more money the cap allows (0 if not matched and capped).
func (o MatchOffer) CapLeftPence() int64 {
	if !o.Money() {
		return 0
	}
	return max(o.LeftPence(), 0)
}

// Spent says the caps have been reached in every half that's on.
func (o MatchOffer) Spent() bool {
	return (!o.Money() || o.LeftPence() == 0) && (!o.Points() || o.LeftPoints() == 0)
}

func capLeft(limit, held int64) int64 {
	if limit <= 0 {
		return -1
	}
	return max(limit-held, 0)
}

func matchAmount(earned, add, per, left int64) int64 {
	if earned <= 0 || add <= 0 || per <= 0 {
		return 0
	}
	m := earned * add / per
	if left >= 0 {
		m = min(m, left)
	}
	return m
}

func newMatchOffer(row dbq.MatchOffer, name string, held dbq.MatchHeldForPersonRow) MatchOffer {
	return MatchOffer{
		AddPence: row.AddPence, PerPence: row.PerPence, AddPoints: row.AddPoints, PerPoints: row.PerPoints,
		CapPence: row.CapPence, CapPoints: row.CapPoints, SetBy: row.SetBy, SetByName: name,
		HeldPence: held.Pence, HeldPoints: held.Points,
	}
}

func loadOffer(ctx context.Context, q *dbq.Queries, householdID, personID int64) (*MatchOffer, error) {
	row, err := q.GetMatchOffer(ctx, dbq.GetMatchOfferParams{HouseholdID: householdID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("match offer: %w", err)
	}
	by, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: row.SetBy})
	if err != nil {
		return nil, fmt.Errorf("match offer by: %w", err)
	}
	held, err := q.MatchHeldForPerson(ctx, dbq.MatchHeldForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return nil, fmt.Errorf("match held: %w", err)
	}
	o := newMatchOffer(row, by.Name, held)
	return &o, nil
}

// MatchOffer returns personID's match offer, or nil if they have none.
func (s *Service) MatchOffer(ctx context.Context, householdID, personID int64) (*MatchOffer, error) {
	return loadOffer(ctx, s.st.Q, householdID, personID)
}

func goalFor(ctx context.Context, q *dbq.Queries, householdID, personID int64) (*dbq.ListGoalsRow, error) {
	goals, err := q.ListGoals(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("goals: %w", err)
	}
	for _, g := range goals {
		if g.PersonID == personID {
			return &g, nil
		}
	}
	return nil, nil
}

func spendable(ctx context.Context, q *dbq.Queries, householdID, personID int64) (points, pence int64, err error) {
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

func matchEntry(ctx context.Context, q *dbq.Queries, householdID, personID int64, completionID *int64, rewardID, goalSetAt, points, pence int64, note string, by *int64, now int64) error {
	e, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID: householdID, PersonID: personID, Points: points, Pence: pence,
		Kind: "match", Note: note, CreatedBy: by, CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("match ledger: %w", err)
	}
	if err := q.InsertMatchEntry(ctx, dbq.InsertMatchEntryParams{
		HouseholdID: householdID, PersonID: personID, LedgerEntryID: e.ID, CompletionID: completionID,
		RewardID: rewardID, GoalSetAt: goalSetAt, Points: points, Pence: pence, CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("match entry: %w", err)
	}
	return nil
}

func (s *Service) matchCompletion(ctx context.Context, q *dbq.Queries, c dbq.Completion, now int64) error {
	if c.Points <= 0 && c.Pence <= 0 {
		return nil
	}
	if !s.moduleOn(ctx, q, c.HouseholdID, moduleRewards) {
		return nil
	}
	p, err := getPerson(ctx, q, c.HouseholdID, c.PersonID)
	if err != nil || p.Kind == "adult" {
		return nil //nolint:nilerr // an archived person just isn't matched
	}
	offer, err := loadOffer(ctx, q, c.HouseholdID, c.PersonID)
	if err != nil || offer == nil {
		return err
	}
	goal, err := goalFor(ctx, q, c.HouseholdID, c.PersonID)
	if err != nil || goal == nil || goal.Waiting != 0 {
		return err
	}
	havePoints, havePence, err := spendable(ctx, q, c.HouseholdID, c.PersonID)
	if err != nil {
		return err
	}
	needPoints := max(goal.CostPoints-max(havePoints, 0), 0)
	needPence := max(goal.CostPence-max(havePence, 0), 0)

	points := matchAmount(c.Points, offer.AddPoints, offer.PerPoints, offer.LeftPoints())
	points = min(points, needPoints)
	var pence int64
	if s.moduleOn(ctx, q, c.HouseholdID, moduleMoney) {
		pence = min(matchAmount(c.Pence, offer.AddPence, offer.PerPence, offer.LeftPence()), needPence)
	}
	if points <= 0 && pence <= 0 {
		return nil
	}
	return matchEntry(ctx, q, c.HouseholdID, c.PersonID, &c.ID, goal.ID, goal.SetAt,
		max(points, 0), max(pence, 0), matchNotePrefix+offer.SetByName, &offer.SetBy, now)
}

func takeBack(ctx context.Context, q *dbq.Queries, householdID, personID int64, completionID *int64, rewardID, goalSetAt, points, pence int64, note string, now int64) (bool, error) {
	if points <= 0 && pence <= 0 {
		return false, nil
	}
	havePoints, havePence, err := spendable(ctx, q, householdID, personID)
	if err != nil {
		return false, err
	}
	points = min(max(points, 0), max(havePoints, 0))
	pence = min(max(pence, 0), max(havePence, 0))
	if points == 0 && pence == 0 {
		return false, nil
	}
	err = matchEntry(ctx, q, householdID, personID, completionID, rewardID, goalSetAt, -points, -pence, note, nil, now)
	return err == nil, err
}

func (s *Service) takeBackGoalMatch(ctx context.Context, q *dbq.Queries, householdID int64, p dbq.Person) error {
	if p.GoalRewardID == nil || p.GoalSetAt == nil {
		return nil
	}
	m, err := q.MatchForGoal(ctx, dbq.MatchForGoalParams{
		HouseholdID: householdID, PersonID: p.ID, RewardID: *p.GoalRewardID, GoalSetAt: *p.GoalSetAt,
	})
	if err != nil {
		return fmt.Errorf("goal match: %w", err)
	}
	now := s.now().Unix()
	took, err := takeBack(ctx, q, householdID, p.ID, nil, *p.GoalRewardID, *p.GoalSetAt, m.Points, m.Pence, matchTakenBackGoal, now)
	if err != nil || !took {
		return err
	}
	return store.LogActivity(ctx, q, store.Activity{
		HouseholdID: householdID, Verb: "match.taken_back", SubjectType: "person", SubjectID: p.ID, At: s.now(),
	})
}

func takeBackCompletionMatch(ctx context.Context, q *dbq.Queries, c dbq.Completion, title string, now int64) error {
	m, err := q.MatchForCompletion(ctx, dbq.MatchForCompletionParams{HouseholdID: c.HouseholdID, CompletionID: &c.ID})
	if err != nil {
		return fmt.Errorf("chore match: %w", err)
	}
	note := matchTakenBackChore + title
	if title == "" {
		note = matchTakenBackPrefix
	}
	_, err = takeBack(ctx, q, c.HouseholdID, c.PersonID, &c.ID, m.RewardID, m.GoalSetAt, m.Points, m.Pence, note, now)
	return err
}

// MatchInput is an adult's match offer for a child, as entered.
type MatchInput struct {
	AddPence, PerPence   int64
	AddPoints, PerPoints int64
	CapPence, CapPoints  int64
}

// SaveMatchOffer sets up or changes personID's match offer.
func (s *Service) SaveMatchOffer(ctx context.Context, householdID, personID, actorID int64, in MatchInput) error {
	errs := valid.Errors{}
	for _, f := range []struct {
		name     string
		add, per int64
	}{{"match_money", in.AddPence, in.PerPence}, {"match_points", in.AddPoints, in.PerPoints}} {
		switch {
		case f.add < 0 || f.per < 0:
			errs.Add(f.name, "Amounts can't be less than nothing.")
		case f.add > 0 && f.per == 0:
			errs.Add(f.name, "Say how much they need to earn for it.")
		}
	}
	if in.CapPence < 0 {
		errs.Add("match_cap_money", "The limit can't be less than nothing.")
	}
	if in.CapPoints < 0 {
		errs.Add("match_cap_points", "The limit can't be less than nothing.")
	}
	if errs.Err() == nil && in.AddPence == 0 && in.AddPoints == 0 {
		errs.Add("match_money", "Add an amount for money or points, or stop matching.")
	}
	if err := errs.Err(); err != nil {
		return err
	}
	if in.AddPence == 0 {
		in.PerPence = 0
	}
	if in.AddPoints == 0 {
		in.PerPoints = 0
	}
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, actorID); err != nil {
			return err
		}
		p, err := getPerson(ctx, q, householdID, personID)
		if err != nil {
			return err
		}
		if p.Kind == "adult" {
			return ErrNotFound
		}
		if err := q.UpsertMatchOffer(ctx, dbq.UpsertMatchOfferParams{
			PersonID: personID, HouseholdID: householdID,
			AddPence: in.AddPence, PerPence: in.PerPence, AddPoints: in.AddPoints, PerPoints: in.PerPoints,
			CapPence: in.CapPence, CapPoints: in.CapPoints, SetBy: actorID, UpdatedAt: s.now().Unix(),
		}); err != nil {
			return fmt.Errorf("save match offer: %w", err)
		}
		return s.activity(ctx, q, householdID, actorID, "match.set", personID)
	})
}

// RemoveMatchOffer stops matching for personID.
func (s *Service) RemoveMatchOffer(ctx context.Context, householdID, personID, actorID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, actorID); err != nil {
			return err
		}
		n, err := q.DeleteMatchOffer(ctx, dbq.DeleteMatchOfferParams{HouseholdID: householdID, PersonID: personID})
		if err != nil {
			return fmt.Errorf("remove match offer: %w", err)
		}
		if n == 0 {
			return nil
		}
		return s.activity(ctx, q, householdID, actorID, "match.off", personID)
	})
}

type goalMatches struct {
	offers map[int64]MatchOffer
	held   map[int64]dbq.ListGoalMatchesRow
}

func (s *Service) loadGoalMatches(ctx context.Context, q *dbq.Queries, householdID int64) (goalMatches, error) {
	gm := goalMatches{offers: map[int64]MatchOffer{}, held: map[int64]dbq.ListGoalMatchesRow{}}
	offers, err := q.ListMatchOffers(ctx, householdID)
	if err != nil {
		return gm, fmt.Errorf("match offers: %w", err)
	}
	for _, o := range offers {
		held, err := q.MatchHeldForPerson(ctx, dbq.MatchHeldForPersonParams{HouseholdID: householdID, PersonID: o.PersonID})
		if err != nil {
			return gm, fmt.Errorf("match held: %w", err)
		}
		gm.offers[o.PersonID] = newMatchOffer(dbq.MatchOffer{
			AddPence: o.AddPence, PerPence: o.PerPence, AddPoints: o.AddPoints, PerPoints: o.PerPoints,
			CapPence: o.CapPence, CapPoints: o.CapPoints, SetBy: o.SetBy,
		}, o.SetByName, held)
	}
	rows, err := q.ListGoalMatches(ctx, householdID)
	if err != nil {
		return gm, fmt.Errorf("goal matches: %w", err)
	}
	for _, r := range rows {
		gm.held[r.PersonID] = r
	}
	return gm, nil
}

func (gm goalMatches) apply(g *Goal, p dbq.Person, money bool) {
	if p.Kind == "adult" {
		return
	}
	if h, ok := gm.held[p.ID]; ok {
		g.MatchedPoints = min(max(h.Points, 0), g.HavePoints)
		g.MatchedPence = min(max(h.Pence, 0), g.HavePence)
	}
	if o, ok := gm.offers[p.ID]; ok {
		if !money {
			o.AddPence = 0
		}
		if g.CostPence == 0 {
			o.AddPence = 0
		}
		if g.CostPoints == 0 {
			o.AddPoints = 0
		}
		if o.Money() || o.Points() {
			g.Offer = &o
		}
	}
	g.MatchPercent = matchPercent(*g)
}

func matchPercent(g Goal) int {
	if g.Waiting || g.Percent == 0 {
		return 0
	}
	share := func(matched, have int64) int {
		if have <= 0 {
			return 0
		}
		return int(int64(g.Percent) * matched / have)
	}
	switch {
	case g.CostPoints > 0 && g.CostPence > 0:
		if g.HavePoints*100/g.CostPoints <= g.HavePence*100/g.CostPence {
			return share(g.MatchedPoints, g.HavePoints)
		}
		return share(g.MatchedPence, g.HavePence)
	case g.CostPoints > 0:
		return share(g.MatchedPoints, g.HavePoints)
	case g.CostPence > 0:
		return share(g.MatchedPence, g.HavePence)
	}
	return 0
}
