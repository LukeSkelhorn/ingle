package chores

import (
	"errors"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

func TestPersonSummary(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	weekStart := time.Date(2026, 11, 2, 0, 0, 0, 0, london) // Monday

	entry := func(p dbq.Person, kind string, points, pence int64, at time.Time) {
		t.Helper()
		if _, err := f.st.Q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
			HouseholdID: f.hh, PersonID: p.ID, Points: points, Pence: pence, Kind: kind, CreatedAt: at.Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	entry(f.kid, "completion", 10, 50, weekStart.Add(-time.Second)) // last week
	entry(f.kid, "completion", 5, 20, weekStart)                    // first second of this week
	entry(f.kid, "completion", 3, 0, weekStart.Add(26*time.Hour))
	entry(f.kid, "adjustment", -3, 0, weekStart.Add(27*time.Hour))   // reversed
	entry(f.kid, "redemption", -8, -40, weekStart.Add(28*time.Hour)) // spending
	entry(f.kid2, "completion", 100, 0, weekStart.Add(time.Hour))    // someone else

	got, err := f.svc.PersonSummary(ctx, f.hh, f.kid.ID, weekStart)
	if err != nil {
		t.Fatal(err)
	}
	if got.Person.ID != f.kid.ID {
		t.Errorf("person = %d, want %d", got.Person.ID, f.kid.ID)
	}
	if got.Points != 7 || got.Pence != 30 {
		t.Errorf("balance = %d pts %dp, want 7 pts 30p", got.Points, got.Pence)
	}
	if got.WeekPoints != 5 || got.WeekPence != 20 {
		t.Errorf("this week = %d pts %dp, want 5 pts 20p", got.WeekPoints, got.WeekPence)
	}

	if _, err := f.svc.PersonSummary(ctx, f.hh+1, f.kid.ID, weekStart); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household: err = %v, want ErrNotFound", err)
	}
	if err := f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{HouseholdID: f.hh, ID: f.kid2.ID, ArchivedAt: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PersonSummary(ctx, f.hh, f.kid2.ID, weekStart); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived: err = %v, want ErrNotFound", err)
	}
}
