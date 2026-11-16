package chores

import (
	"strconv"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func (f *fixture) keepHistory(months int) {
	f.t.Helper()
	if err := f.st.Q.InsertDefaultSettings(f.t.Context(), dbq.InsertDefaultSettingsParams{
		HouseholdID: f.hh, Timezone: "Europe/London", UpdatedAt: 1,
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.SQL().ExecContext(f.t.Context(),
		"UPDATE household_settings SET keep_history_months = ? WHERE household_id = ?", months, f.hh); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) clearHistory() Cleared {
	f.t.Helper()
	c, err := f.svc.ClearHistory(f.t.Context(), f.hh)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.st.SQL().QueryRowContext(f.t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) countWhere(table, where string) int {
	f.t.Helper()
	return f.count(table + " WHERE " + where)
}

// money is everything a clear-out must leave alone for p.
type money struct {
	Balance                       dbq.BalanceForPersonRow
	Interest                      dbq.InterestTotalsForPersonRow
	MatchPoints, MatchPence       int64
	Points, Pence, Held, HeldPens int64
	GoalPercent, GoalMatched      int64
}

func (f *fixture) money(p dbq.Person) money {
	f.t.Helper()
	ctx := f.t.Context()
	in, err := f.st.Q.InterestTotalsForPerson(ctx, dbq.InterestTotalsForPersonParams{HouseholdID: f.hh, PersonID: p.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	mp, mpence, _ := f.matched(p)
	s := f.summary(p)
	m := money{
		Balance: f.balance(p), Interest: in, MatchPoints: mp, MatchPence: mpence,
		Points: s.Points, Pence: s.Pence, Held: s.HeldPoints, HeldPens: s.HeldPence,
	}
	if s.Goal != nil {
		m.GoalPercent, m.GoalMatched = int64(s.Goal.Percent), s.Goal.MatchedPence
	}
	return m
}

// oldHistory fills the household with a bit of everything on 2 November
// 2026, some of it decided and some still waiting.
func (f *fixture) oldHistory() {
	f.t.Helper()
	ctx := f.t.Context()
	// Alfie saves for Lego, matched by Mum, and does chores.
	f.goal(f.kid, f.reward("Lego", 1000, 5000))
	f.offer(f.kid, tenPence)
	f.doChore(f.kid, 15, 50, false)
	approved := f.doChore(f.kid, 10, 100, true)
	if err := f.svc.Approve(ctx, f.hh, approved.ID, f.adult.ID); err != nil {
		f.t.Fatal(err)
	}
	f.doChore(f.kid, 5, 20, true) // still waiting for a grown-up
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, -2, "Shouted"); err != nil {
		f.t.Fatal(err)
	}
	// A reward claimed and approved, and one still waiting.
	sweets := f.reward("Sweets", 5, 0)
	d, err := f.svc.ClaimReward(ctx, f.hh, sweets.ID, f.kid.ID, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.ApproveRedemption(ctx, f.hh, d.ID, f.adult.ID); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.ClaimReward(ctx, f.hh, sweets.ID, f.kid.ID, nil); err != nil {
		f.t.Fatal(err)
	}
	// Interest, which the clear-out leaves as it is.
	if _, err := f.st.Q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
		HouseholdID: f.hh, PersonID: f.kid.ID, Points: 1, Pence: 7, Kind: "interest", Note: InterestNote, CreatedAt: f.svc.now().Unix(),
	}); err != nil {
		f.t.Fatal(err)
	}
	// Isla has money in the bank: one request paid, and one waiting.
	f.earn(f.kid2, 40, 1000)
	req, err := f.svc.RequestTransfer(ctx, f.hh, f.kid2.ID, 300)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.ApproveTransfer(ctx, f.hh, req.ID, f.adult.ID); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.RequestTransfer(ctx, f.hh, f.kid2.ID, 200); err != nil {
		f.t.Fatal(err)
	}
}

func TestClearHistory(t *testing.T) {
	f := newFixture(t)
	f.oldHistory()
	// Fourteen months later, with one recent line.
	f.clock(time.Date(2028, 1, 10, 9, 0, 0, 0, london))
	f.earnAt(f.kid, 3, 30, time.Date(2027, 12, 20, 12, 0, 0, 0, london))
	before := map[string]money{"Alfie": f.money(f.kid), "Isla": f.money(f.kid2)}
	ledger := f.count("ledger_entries")
	kept := map[string]int{}
	for _, kind := range []string{"interest", "match"} {
		kept[kind] = f.countWhere("ledger_entries", "kind = '"+kind+"'")
	}

	// Everything, then 24 months: nothing is old enough.
	f.keepHistory(0)
	if c := f.clearHistory(); c.Any() {
		t.Errorf("Everything cleared %+v", c)
	}
	f.keepHistory(24)
	if c := f.clearHistory(); c.Any() {
		t.Errorf("24 months cleared %+v", c)
	}
	if n := f.count("ledger_entries"); n != ledger {
		t.Fatalf("ledger lines %d, want %d", n, ledger)
	}

	f.keepHistory(12)
	c := f.clearHistory()
	if !c.Any() || c.Completions != 2 || c.Redemptions != 1 || c.Transfers != 1 || c.Activity == 0 {
		t.Errorf("cleared %+v", c)
	}
	after := map[string]money{"Alfie": f.money(f.kid), "Isla": f.money(f.kid2)}
	for who := range before {
		if before[who] != after[who] {
			t.Errorf("%s's money changed:\n before %+v\n after  %+v", who, before[who], after[who])
		}
	}

	// Each child has one Brought forward line; interest and match lines,
	// the recent line and the waiting ones are still there.
	if n := f.countWhere("ledger_entries", "kind = 'brought_forward'"); n != 2 {
		t.Errorf("%d brought forward lines, want 2", n)
	}
	for _, kind := range []string{"interest", "match"} {
		if n := f.countWhere("ledger_entries", "kind = '"+kind+"'"); n != kept[kind] || n == 0 {
			t.Errorf("%d %s lines, want %d", n, kind, kept[kind])
		}
	}
	if n := f.countWhere("ledger_entries", "kind = 'completion'"); n != 1 {
		t.Errorf("%d completion lines, want just the recent one", n)
	}
	if n := f.countWhere("redemptions", "status = 'pending'"); n != 1 || f.count("redemptions") != 1 {
		t.Errorf("claims left: %d waiting of %d", n, f.count("redemptions"))
	}
	if n := f.countWhere("transfer_requests", "status = 'pending'"); n != 1 || f.count("transfer_requests") != 1 {
		t.Errorf("bank requests left: %d waiting of %d", n, f.count("transfer_requests"))
	}
	if n := f.countWhere("completions", "status = 'pending'"); n != 1 || f.count("completions") != 1 {
		t.Errorf("completions left: %d waiting of %d", n, f.count("completions"))
	}
	// The waiting completion keeps its chore copy; the rest go.
	if n := f.count("chore_occurrences"); n != 1 {
		t.Errorf("%d chore copies left, want 1", n)
	}
	if n := f.countWhere("activity", "created_at < "+strconv.FormatInt(HistoryCutoff(f.svc.now(), london, 12).Unix(), 10)); n != 0 {
		t.Errorf("%d old activity lines left", n)
	}

	// My money: Brought forward is the last line, dated before the cutoff.
	items, next := f.history(f.kid, 1<<62)
	for next != 0 {
		var more []HistoryItem
		more, next = f.history(f.kid, next)
		items = append(items, more...)
	}
	last := items[len(items)-1]
	if last.Kind != "brought_forward" || last.At >= HistoryCutoff(f.svc.now(), london, 12).Unix() {
		t.Errorf("last history line = %+v, want Brought forward", last)
	}

	// Again: nothing more to do, and the money's the same.
	if c := f.clearHistory(); c.Any() {
		t.Errorf("second run cleared %+v", c)
	}
	if got := f.money(f.kid); got != before["Alfie"] {
		t.Errorf("Alfie's money after a second run = %+v", got)
	}

	// A month on, last month's line folds into Brought forward too.
	f.clock(time.Date(2029, 1, 1, 9, 0, 0, 0, london))
	if c := f.clearHistory(); c.Folded != 1 {
		t.Errorf("a year on folded %d, want 1", c.Folded)
	}
	if got := f.money(f.kid); got.Balance != before["Alfie"].Balance {
		t.Errorf("Alfie's balance a year on = %+v", got.Balance)
	}
}
