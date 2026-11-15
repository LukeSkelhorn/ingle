package chores

import (
	"errors"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

func (f *fixture) clock(t time.Time) { f.svc.now = func() time.Time { return t } }

func (f *fixture) earnAt(p dbq.Person, points, pence int64, at time.Time) {
	f.t.Helper()
	if _, err := f.st.Q.InsertLedgerEntry(f.t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: f.hh, PersonID: p.ID, Points: points, Pence: pence, Kind: "completion", CreatedAt: at.Unix(),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) interestOn(in InterestInput) {
	f.t.Helper()
	in.On = true
	if err := f.st.Q.InsertDefaultSettings(f.t.Context(), dbq.InsertDefaultSettingsParams{
		HouseholdID: f.hh, Timezone: "Europe/London", UpdatedAt: 1,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.SaveInterest(f.t.Context(), f.hh, f.adult.ID, in); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) payInterest() int {
	f.t.Helper()
	n, err := f.svc.PayInterest(f.t.Context(), f.hh)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) interestLines(p dbq.Person) []dbq.ListLedgerHistoryRow {
	f.t.Helper()
	rows, err := f.st.Q.ListLedgerHistory(f.t.Context(), dbq.ListLedgerHistoryParams{
		HouseholdID: f.hh, PersonID: p.ID, BeforeID: 1 << 62, Lim: 100,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []dbq.ListLedgerHistoryRow
	for _, r := range rows {
		if r.Kind == "interest" {
			out = append(out, r)
		}
	}
	return out
}

func day(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, london) }

func TestInterestOnLowestBalance(t *testing.T) {
	f := newFixture(t)
	// Before interest is on, Alfie had nothing; that doesn't count.
	f.earnAt(f.kid, 100, 1000, day(11, 2, 8))
	f.interestOn(InterestInput{RateBP: 1000, Money: true, Points: true}) // 10% a month, at 09:00
	f.earnAt(f.kid, 0, -400, day(11, 10, 12))
	f.earnAt(f.kid, 50, 600, day(11, 20, 12))

	// Nothing is paid for a month that hasn't finished.
	f.clock(day(11, 30, 23))
	if n := f.payInterest(); n != 0 {
		t.Fatalf("paid %d before the month ended", n)
	}

	f.clock(day(12, 1, 1))
	if n := f.payInterest(); n != 1 {
		t.Fatalf("paid %d, want 1", n)
	}
	// Lowest was 600p (after spending 400) and 100 pts.
	lines := f.interestLines(f.kid)
	if len(lines) != 1 || lines[0].Pence != 60 || lines[0].Points != 10 || lines[0].Note != InterestNote {
		t.Fatalf("interest = %+v", lines)
	}
	if got := time.Unix(lines[0].CreatedAt, 0).In(london); !got.Equal(day(12, 1, 0).Add(-time.Second)) {
		t.Errorf("dated %v, want the month's last second", got)
	}
	// Safe to run again.
	if n := f.payInterest(); n != 0 {
		t.Errorf("second run paid %d", n)
	}
	if s := f.summary(f.kid); s.Pence != 1260 || s.Points != 160 {
		t.Errorf("balance = %d pts %dp", s.Points, s.Pence)
	}
	// It isn't "earned this week".
	if pts, p, _ := f.svc.InterestTotals(t.Context(), f.hh, f.kid.ID); pts != 10 || p != 60 {
		t.Errorf("totals = %d pts %dp", pts, p)
	}
	// Isla had nothing, and adults never earn interest.
	if got := f.interestLines(f.kid2); len(got) != 0 {
		t.Errorf("Isla got %+v", got)
	}
	if got := f.interestLines(f.adult); len(got) != 0 {
		t.Errorf("Mum got %+v", got)
	}
}

func TestInterestNegativeBalanceEarnsNothing(t *testing.T) {
	f := newFixture(t)
	f.interestOn(InterestInput{RateBP: 1000, Money: true, Points: true})
	f.earnAt(f.kid, -5, 0, day(11, 3, 9))
	f.earnAt(f.kid, 105, 0, day(11, 4, 9))
	f.clock(day(12, 1, 1))
	f.payInterest()
	if got := f.interestLines(f.kid); len(got) != 0 {
		t.Errorf("interest = %+v", got)
	}
}

func TestInterestCarriesFractionsAndCatchesUp(t *testing.T) {
	f := newFixture(t)
	f.earnAt(f.kid, 0, 50, day(11, 1, 8))
	f.interestOn(InterestInput{RateBP: 100, Money: true}) // 1% of 50p is half a penny

	// Ingle was off over Christmas: November, December and January are paid at once.
	f.clock(time.Date(2027, 2, 3, 10, 0, 0, 0, london))
	if n := f.payInterest(); n != 1 {
		t.Fatalf("paid %d, want 1", n)
	}
	lines := f.interestLines(f.kid)
	if len(lines) != 1 || lines[0].Pence != 1 {
		t.Fatalf("interest = %+v", lines)
	}
	if got := time.Unix(lines[0].CreatedAt, 0).In(london); got.Month() != time.December {
		t.Errorf("paid for %v, want December", got)
	}
	for _, period := range []string{"2026-11", "2026-12", "2027-01"} {
		if n, err := f.st.Q.HasInterestPayment(t.Context(), dbq.HasInterestPaymentParams{
			HouseholdID: f.hh, PersonID: f.kid.ID, Period: period,
		}); err != nil || n != 1 {
			t.Errorf("%s recorded %d times, %v", period, n, err)
		}
	}
	if n := f.payInterest(); n != 0 {
		t.Errorf("second run paid %d", n)
	}
}

func TestInterestCapsAndChoices(t *testing.T) {
	f := newFixture(t)
	f.earnAt(f.kid, 1000, 10000, day(11, 1, 8))
	f.interestOn(InterestInput{RateBP: 1000, Money: true, Points: false, CapPence: 250})
	f.clock(day(12, 1, 1))
	f.payInterest()
	lines := f.interestLines(f.kid)
	if len(lines) != 1 || lines[0].Pence != 250 || lines[0].Points != 0 {
		t.Fatalf("interest = %+v", lines)
	}

	// Points only, with a points cap.
	f.interestOn(InterestInput{RateBP: 1000, Points: true, CapPoints: 30})
	f.clock(time.Date(2027, 1, 1, 1, 0, 0, 0, london))
	f.payInterest()
	lines = f.interestLines(f.kid)
	if len(lines) != 2 || lines[0].Points != 30 || lines[0].Pence != 0 {
		t.Fatalf("interest = %+v", lines)
	}
}

func TestInterestOptOutAndOff(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.earnAt(f.kid, 100, 1000, day(11, 1, 8))
	f.earnAt(f.kid2, 100, 1000, day(11, 1, 8))
	f.interestOn(InterestInput{RateBP: 1000, Money: true, Points: true})
	if err := f.svc.SetInterestOptOut(ctx, f.hh, f.kid2.ID, true); err != nil {
		t.Fatal(err)
	}
	f.clock(day(12, 1, 1))
	if n := f.payInterest(); n != 1 {
		t.Fatalf("paid %d, want 1 (Alfie)", n)
	}
	// Opting Isla back in never backdates November.
	if err := f.svc.SetInterestOptOut(ctx, f.hh, f.kid2.ID, false); err != nil {
		t.Fatal(err)
	}
	if n := f.payInterest(); n != 0 || len(f.interestLines(f.kid2)) != 0 {
		t.Errorf("Isla was paid for a month she'd opted out of")
	}

	// Off: December is never paid, and turning it back on starts afresh.
	if err := f.svc.SaveInterest(ctx, f.hh, f.adult.ID, InterestInput{On: false, Money: true, Points: true}); err != nil {
		t.Fatal(err)
	}
	f.clock(time.Date(2027, 1, 1, 1, 0, 0, 0, london))
	if n := f.payInterest(); n != 0 {
		t.Errorf("paid %d while off", n)
	}
	in, _ := f.svc.InterestSettings(ctx, f.hh)
	if in.On() || in.RateBP != 1000 {
		t.Errorf("off: %+v, want off with the rate kept", in)
	}
	f.interestOn(InterestInput{RateBP: 1000, Money: true, Points: true})
	f.clock(time.Date(2027, 2, 1, 1, 0, 0, 0, london))
	if n := f.payInterest(); n != 2 {
		t.Errorf("paid %d for January, want 2", n)
	}
	if got := f.interestLines(f.kid); len(got) != 2 {
		t.Errorf("Alfie's interest = %+v, want November and January", got)
	}
}

func TestSaveInterestValidates(t *testing.T) {
	f := newFixture(t)
	err := f.svc.SaveInterest(t.Context(), f.hh, f.adult.ID, InterestInput{On: true, RateBP: 0, CapPence: -1})
	var ve valid.Errors
	if !errors.As(err, &ve) || ve["interest_rate"] == "" || ve["interest_what"] == "" || ve["interest_cap_money"] == "" {
		t.Errorf("err = %v", err)
	}
	if err := f.svc.SaveInterest(t.Context(), f.hh, f.adult.ID, InterestInput{On: true, RateBP: 1001, Money: true}); err == nil {
		t.Error("11% a month was allowed")
	}

	// Saving again while on keeps the first month's start.
	f.interestOn(InterestInput{RateBP: 100, Money: true})
	first, _ := f.svc.InterestSettings(t.Context(), f.hh)
	f.clock(day(11, 20, 9))
	f.interestOn(InterestInput{RateBP: 200, Money: true})
	again, _ := f.svc.InterestSettings(t.Context(), f.hh)
	if !again.Since.Equal(*first.Since) || again.RateBP != 200 {
		t.Errorf("since moved: %v then %v", first.Since, again.Since)
	}
}

func TestRateHelpers(t *testing.T) {
	for in, want := range map[string]int64{"1": 100, "0.5": 50, "1.25%": 125, ".75": 75, "10": 1000} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("ParseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "1.234", "-1", "1.x"} {
		if _, err := ParseRate(in); err == nil {
			t.Errorf("ParseRate(%q) accepted", in)
		}
	}
	for bp, want := range map[int64]string{100: "1", 50: "0.5", 125: "1.25", 1000: "10"} {
		if got := FormatRate(bp); got != want {
			t.Errorf("FormatRate(%d) = %q, want %q", bp, got, want)
		}
	}
	if got := YearlyRate(100); got != "12.7" {
		t.Errorf("YearlyRate(100) = %q", got)
	}
	// £10 at 1% a month for a year, compounding by the penny.
	if got := InterestAfter(1000, 100, 0, 12); got != 1126 {
		t.Errorf("InterestAfter = %d", got)
	}
	if got := InterestAfter(1000, 1000, 50, 2); got != 1100 {
		t.Errorf("capped InterestAfter = %d", got)
	}
}
