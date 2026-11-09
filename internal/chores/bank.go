package chores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// BankStale is how old a bank balance gets before Admin home asks an adult
// to check it.
const BankStale = 14 * 24 * time.Hour

// MaxBankLabel is the longest account name, e.g. "Monzo".
const MaxBankLabel = 40

// Bank sources.
const (
	BankManual = "manual"
	BankMonzo  = "monzo"
)

// Bank is a child's real bank balance as Ingle last knew it.
type Bank struct {
	Label     string
	Pence     int64
	Source    string
	UpdatedAt time.Time // when someone last checked the real bank
}

// StaleBank is a child whose bank balance nobody has checked for a while.
type StaleBank struct {
	PersonID  int64
	Name      string
	UpdatedAt time.Time
}

// PersonBank is personID's bank balance, or nil if they have none set up.
func (s *Service) PersonBank(ctx context.Context, householdID, personID int64) (*Bank, error) {
	b, err := s.st.Q.GetPersonBank(ctx, dbq.GetPersonBankParams{HouseholdID: householdID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bank: %w", err)
	}
	return &Bank{Label: b.Label, Pence: b.Pence, Source: b.Source, UpdatedAt: time.Unix(b.UpdatedAt, 0)}, nil
}

// SetBank is an adult typing in what's in a child's bank now. Bad input
// returns valid.Errors on "bank_label" and "bank_pence".
func (s *Service) SetBank(ctx context.Context, householdID, personID int64, label string, pence, adultID int64) error {
	label = strings.TrimSpace(label)
	errs := valid.Errors{}
	if n := len([]rune(label)); n < 1 || n > MaxBankLabel {
		errs.Add("bank_label", fmt.Sprintf("Name the account, e.g. Monzo (up to %d characters).", MaxBankLabel))
	}
	if pence < 0 {
		errs.Add("bank_pence", "The balance can't be less than nothing.")
	}
	if err := errs.Err(); err != nil {
		return err
	}
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		if err := requireChild(ctx, q, householdID, personID); err != nil {
			return err
		}
		if err := q.UpsertPersonBank(ctx, dbq.UpsertPersonBankParams{
			PersonID: personID, HouseholdID: householdID, Label: label, Pence: pence,
			Source: BankManual, UpdatedAt: s.now().Unix(), UpdatedBy: &adultID,
		}); err != nil {
			return fmt.Errorf("set bank: %w", err)
		}
		return s.activity(ctx, q, householdID, adultID, "bank.updated", personID)
	})
}

// RemoveBank stops showing personID's bank balance.
func (s *Service) RemoveBank(ctx context.Context, householdID, personID, adultID int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := requireAdult(ctx, q, householdID, adultID); err != nil {
			return err
		}
		n, err := q.DeletePersonBank(ctx, dbq.DeletePersonBankParams{HouseholdID: householdID, PersonID: personID})
		if err != nil {
			return fmt.Errorf("remove bank: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return s.activity(ctx, q, householdID, adultID, "bank.removed", personID)
	})
}

// StaleBanks are the children whose bank balance is older than BankStale.
func (s *Service) StaleBanks(ctx context.Context, householdID int64) ([]StaleBank, error) {
	rows, err := s.st.Q.StalePersonBanks(ctx, dbq.StalePersonBanksParams{
		HouseholdID: householdID, BeforeUnix: s.now().Add(-BankStale).Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("stale banks: %w", err)
	}
	out := make([]StaleBank, len(rows))
	for i, r := range rows {
		out[i] = StaleBank{PersonID: r.PersonID, Name: r.Name, UpdatedAt: time.Unix(r.UpdatedAt, 0)}
	}
	return out, nil
}

func requireChild(ctx context.Context, q *dbq.Queries, householdID, personID int64) error {
	p, err := getPerson(ctx, q, householdID, personID)
	if err != nil {
		return err
	}
	if p.Kind != "child" {
		return ErrNotChild
	}
	return nil
}
