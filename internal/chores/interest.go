package chores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// InterestNote is the history line's text for a month's interest.
const InterestNote = "Interest from the Bank of Mum and Dad"

const bpScale = 10000

// Interest is a household's Bank of Mum and Dad settings.
type Interest struct {
	RateBP              int64
	Money, Points       bool
	CapPence, CapPoints int64
	Since               *time.Time
}

// InterestFrom reads the interest settings from a settings row.
func InterestFrom(hs dbq.HouseholdSetting) Interest {
	in := Interest{
		RateBP: hs.InterestRateBp, Money: hs.InterestMoney == 1, Points: hs.InterestPoints == 1,
		CapPence: hs.InterestCapPence, CapPoints: hs.InterestCapPoints,
	}
	if hs.InterestSince != nil {
		t := time.Unix(*hs.InterestSince, 0)
		in.Since = &t
	}
	return in
}

// On reports whether children are earning interest.
func (in Interest) On() bool {
	return in.RateBP > 0 && (in.Money || in.Points) && in.Since != nil
}

// Earns reports whether p earns interest: children only, unless an adult switched it off for them.
func (in Interest) Earns(p dbq.Person) bool {
	return in.On() && p.Kind == "child" && p.InterestOptOut == 0 && p.ArchivedAt == nil
}

func accrue(lowest, bp, carry, cap int64) (whole, left int64) {
	lowest = max(lowest, 0)
	t := lowest*bp + carry
	whole, left = t/bpScale, t%bpScale
	if cap > 0 && whole > cap {
		return cap, 0
	}
	return whole, left
}

// InterestAfter is roughly what balance becomes after months of interest.
func InterestAfter(balance, bp, cap int64, months int) int64 {
	var carry int64
	for range months {
		var whole int64
		whole, carry = accrue(balance, bp, carry, cap)
		balance += whole
	}
	return balance
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// PayInterest pays each child every finished, unpaid month of interest; it is safe to rerun.
func (s *Service) PayInterest(ctx context.Context, householdID int64) (int, error) {
	hs, err := s.st.Q.GetSettings(ctx, householdID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("settings: %w", err)
	}
	in := InterestFrom(hs)
	if !in.On() {
		return 0, nil
	}
	loc := s.loc(ctx, householdID)
	now := s.now()
	since := in.Since.In(loc)
	thisMonth := monthStart(now.In(loc))

	people, err := s.st.Q.ListPeople(ctx, householdID)
	if err != nil {
		return 0, fmt.Errorf("people: %w", err)
	}
	paid := 0
	for m := monthStart(since); m.Before(thisMonth); m = m.AddDate(0, 1, 0) {
		end := m.AddDate(0, 1, 0)
		from := m
		if since.After(from) {
			from = since
		}
		for _, p := range people {
			if p.Kind != "child" {
				continue
			}
			ok, err := s.payMonth(ctx, householdID, p, in, m.Format("2006-01"), from, end, now)
			if err != nil {
				return paid, fmt.Errorf("interest for %d in %s: %w", p.ID, m.Format("2006-01"), err)
			}
			if ok {
				paid++
			}
		}
	}
	return paid, nil
}

func (s *Service) payMonth(ctx context.Context, hh int64, p dbq.Person, in Interest, period string, from, end, now time.Time) (bool, error) {
	paid := false
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		done, err := q.HasInterestPayment(ctx, dbq.HasInterestPaymentParams{HouseholdID: hh, PersonID: p.ID, Period: period})
		if err != nil {
			return fmt.Errorf("check: %w", err)
		}
		if done > 0 {
			return nil
		}
		var carryPoints, carryPence int64
		last, err := q.LastInterestPayment(ctx, dbq.LastInterestPaymentParams{HouseholdID: hh, PersonID: p.ID})
		switch {
		case err == nil:
			carryPoints, carryPence = last.CarryPoints, last.CarryPence
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("carry: %w", err)
		}
		row := dbq.InsertInterestPaymentParams{
			HouseholdID: hh, PersonID: p.ID, Period: period,
			CarryPoints: carryPoints, CarryPence: carryPence, CreatedAt: now.Unix(),
		}
		if in.Earns(p) {
			lowPoints, lowPence, err := lowestBalance(ctx, q, hh, p.ID, from, end)
			if err != nil {
				return err
			}
			if in.Points {
				row.Points, row.CarryPoints = accrue(lowPoints, in.RateBP, carryPoints, in.CapPoints)
			}
			if in.Money {
				row.Pence, row.CarryPence = accrue(lowPence, in.RateBP, carryPence, in.CapPence)
			}
		}
		if row.Points > 0 || row.Pence > 0 {
			// Dated the month's last second, so it counts towards next month's balance.
			e, err := q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
				HouseholdID: hh, PersonID: p.ID, Points: row.Points, Pence: row.Pence,
				Kind: "interest", Note: InterestNote, CreatedAt: end.Unix() - 1,
			})
			if err != nil {
				return fmt.Errorf("ledger: %w", err)
			}
			row.LedgerEntryID = &e.ID
			if err := store.LogActivity(ctx, q, store.Activity{
				HouseholdID: hh, Verb: "interest.paid", SubjectType: "person", SubjectID: p.ID, At: now,
			}); err != nil {
				return err
			}
			paid = true
		}
		if err := q.InsertInterestPayment(ctx, row); err != nil {
			return fmt.Errorf("record: %w", err)
		}
		return nil
	})
	return paid, err
}

func lowestBalance(ctx context.Context, q *dbq.Queries, hh, personID int64, from, end time.Time) (points, pence int64, err error) {
	bal, err := q.BalanceBeforeForPerson(ctx, dbq.BalanceBeforeForPersonParams{
		HouseholdID: hh, PersonID: personID, BeforeUnix: from.Unix(),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("opening balance: %w", err)
	}
	changes, err := q.ListLedgerChangesBetween(ctx, dbq.ListLedgerChangesBetweenParams{
		HouseholdID: hh, PersonID: personID, FromUnix: from.Unix(), ToUnix: end.Unix(),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("changes: %w", err)
	}
	curPoints, curPence := bal.Points, bal.Pence
	points, pence = curPoints, curPence
	for _, c := range changes {
		curPoints += c.Points
		curPence += c.Pence
		points, pence = min(points, curPoints), min(pence, curPence)
	}
	return points, pence, nil
}

// InterestTotals is what a person has earned in interest so far.
func (s *Service) InterestTotals(ctx context.Context, householdID, personID int64) (points, pence int64, err error) {
	t, err := s.st.Q.InterestTotalsForPerson(ctx, dbq.InterestTotalsForPersonParams{HouseholdID: householdID, PersonID: personID})
	if err != nil {
		return 0, 0, fmt.Errorf("interest totals: %w", err)
	}
	return t.Points, t.Pence, nil
}

// InterestSettings reads the household's interest settings.
func (s *Service) InterestSettings(ctx context.Context, householdID int64) (Interest, error) {
	hs, err := s.st.Q.GetSettings(ctx, householdID)
	if err != nil {
		return Interest{}, fmt.Errorf("settings: %w", err)
	}
	return InterestFrom(hs), nil
}

// InterestInput is the Bank of Mum and Dad settings form.
type InterestInput struct {
	On                  bool
	RateBP              int64
	Money, Points       bool
	CapPence, CapPoints int64
}

// MaxInterestBP is the highest rate allowed: 10% a month.
const MaxInterestBP = 1000

// SaveInterest validates and saves the interest settings as actorID.
func (s *Service) SaveInterest(ctx context.Context, householdID, actorID int64, in InterestInput) error {
	errs := valid.Errors{}
	if in.On {
		if in.RateBP <= 0 || in.RateBP > MaxInterestBP {
			errs.Add("interest_rate", "Enter a rate between 0.01% and 10% a month.")
		}
		if !in.Money && !in.Points {
			errs.Add("interest_what", "Choose money, points or both.")
		}
	}
	if in.CapPence < 0 {
		errs.Add("interest_cap_money", "Enter an amount like 0.50, or leave it blank for no limit.")
	}
	if in.CapPoints < 0 {
		errs.Add("interest_cap_points", "Enter a whole number of points, or leave it blank for no limit.")
	}
	if err := errs.Err(); err != nil {
		return err
	}
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		cur, err := q.GetSettings(ctx, householdID)
		if err != nil {
			return fmt.Errorf("settings: %w", err)
		}
		rate, since := in.RateBP, cur.InterestSince
		switch {
		case !in.On:
			rate, since = cur.InterestRateBp, nil
		case !InterestFrom(cur).On():
			t := now.Unix()
			since = &t
		}
		if _, err := q.UpdateInterestSettings(ctx, dbq.UpdateInterestSettingsParams{
			InterestRateBp: rate, InterestMoney: boolInt(in.Money), InterestPoints: boolInt(in.Points),
			InterestCapPence: in.CapPence, InterestCapPoints: in.CapPoints, InterestSince: since,
			UpdatedAt: now.Unix(), HouseholdID: householdID,
		}); err != nil {
			return fmt.Errorf("save interest: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "settings.updated",
			SubjectType: "household", SubjectID: householdID, At: now,
		})
	})
}

// SetInterestOptOut switches interest off (or back on) for one child.
func (s *Service) SetInterestOptOut(ctx context.Context, householdID, personID int64, optOut bool) error {
	if err := s.st.Q.SetPersonInterestOptOut(ctx, dbq.SetPersonInterestOptOutParams{
		InterestOptOut: boolInt(optOut), HouseholdID: householdID, ID: personID,
	}); err != nil {
		return fmt.Errorf("interest opt-out: %w", err)
	}
	return nil
}

// ParseRate reads a rate like "1", "0.5" or "1.25%" (a month) as basis points.
func ParseRate(v string) (int64, error) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "%"))
	whole, frac, _ := strings.Cut(v, ".")
	if (whole == "" && frac == "") || len(frac) > 2 {
		return 0, ErrInvalidRate
	}
	frac += strings.Repeat("0", 2-len(frac))
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseUint(whole, 10, 16)
	if err != nil {
		return 0, ErrInvalidRate
	}
	f, err := strconv.ParseUint(frac, 10, 8)
	if err != nil {
		return 0, ErrInvalidRate
	}
	return int64(w)*100 + int64(f), nil
}

// FormatRate writes basis points as a rate like "1" or "0.25".
func FormatRate(bp int64) string {
	s := strconv.FormatInt(bp/100, 10)
	if f := bp % 100; f != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%02d", f), "0")
	}
	return s
}

// YearlyRate is roughly what a monthly rate compounds to over a year, as a percentage.
func YearlyRate(bp int64) string {
	return strconv.FormatFloat((math.Pow(1+float64(bp)/bpScale, 12)-1)*100, 'f', 1, 64)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
