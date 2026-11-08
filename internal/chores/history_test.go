package chores

import (
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

func (f *fixture) history(p dbq.Person, before int64) ([]HistoryItem, int64) {
	f.t.Helper()
	items, next, err := f.svc.History(f.t.Context(), f.hh, p.ID, f.svc.now().AddDate(0, 0, -30), before)
	if err != nil {
		f.t.Fatal(err)
	}
	return items, next
}

func TestHistory(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// A chore earned, a reward claimed (waiting), then bought.
	f.chore("Fed the cat", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 10, pence: 20})
	if _, err := f.svc.Complete(ctx, f.hh, f.onlyOccurrence().ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, -2, "Shouted at Isla"); err != nil {
		t.Fatal(err)
	}
	f.earn(f.kid2, 50, 0) // someone else's

	cinema := f.reward("Cinema trip", 5, 10)
	d, err := f.svc.ClaimReward(ctx, f.hh, cinema.ID, f.kid.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := f.svc.WaitingClaims(ctx, f.hh, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].Kind != "waiting" || waiting[0].Title != "Cinema trip" || waiting[0].Points != -5 || waiting[0].Pence != -10 {
		t.Errorf("waiting = %+v", waiting)
	}
	if items, _ := f.history(f.kid, 0); len(items) != 2 {
		t.Errorf("history before approval = %+v", items)
	}

	if err := f.svc.ApproveRedemption(ctx, f.hh, d.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if waiting, _ := f.svc.WaitingClaims(ctx, f.hh, f.kid.ID); len(waiting) != 0 {
		t.Errorf("still waiting after approval: %+v", waiting)
	}
	items, next := f.history(f.kid, 0)
	if next != 0 {
		t.Errorf("next = %d, want nothing older", next)
	}
	type line struct {
		kind, title, note string
		points, pence     int64
	}
	var got []line
	for _, it := range items {
		got = append(got, line{it.Kind, it.Title, it.Note, it.Points, it.Pence})
	}
	want := []line{
		{"redemption", "Cinema trip", "Cinema trip", -5, -10},
		{"manual", "", "Shouted at Isla", -2, 0},
		{"completion", "Fed the cat", "", 10, 20},
	}
	if len(got) != len(want) {
		t.Fatalf("history = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("history[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHistoryPaging(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	old := f.svc.now().AddDate(0, 0, -40).Unix()
	for i := range HistoryPage + 5 {
		if _, err := f.st.Q.InsertLedgerEntry(ctx, dbq.InsertLedgerEntryParams{
			HouseholdID: f.hh, PersonID: f.kid.ID, Points: int64(i + 1), Kind: "completion", CreatedAt: old + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing in the last 30 days, but there's older history to show.
	items, next := f.history(f.kid, 0)
	if len(items) != 0 || next == 0 {
		t.Fatalf("first page = %d items, next %d", len(items), next)
	}
	page, next := f.history(f.kid, next)
	if len(page) != HistoryPage || page[0].Points != HistoryPage+5 || next != page[len(page)-1].ID {
		t.Fatalf("page 2 = %d items from %d, next %d", len(page), page[0].Points, next)
	}
	rest, next := f.history(f.kid, next)
	if len(rest) != 5 || rest[4].Points != 1 || next != 0 {
		t.Errorf("page 3 = %d items, next %d", len(rest), next)
	}

	// Recent entries come first, then older ones on Show more.
	f.earn(f.kid, 7, 0)
	items, next = f.history(f.kid, 0)
	if len(items) != 1 || items[0].Points != 7 || next != items[0].ID {
		t.Errorf("first page = %+v, next %d", items, next)
	}
	if _, _, err := f.svc.History(ctx, f.hh, 9999, time.Time{}, 0); err == nil {
		t.Error("history for nobody")
	}
}

// A timed chore's history line carries its time; others don't.
func TestHistoryDueTime(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	at := "19:30"
	f.chore("Brush teeth", choreOpts{assignee: &f.kid.ID, points: 1, dueTime: &at})
	if _, err := f.svc.Complete(ctx, f.hh, f.onlyOccurrence().ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdjustPoints(ctx, f.hh, f.adult.ID, f.kid.ID, 3, "Helped out"); err != nil {
		t.Fatal(err)
	}

	items, _ := f.history(f.kid, 0)
	if len(items) != 2 {
		t.Fatalf("history = %+v", items)
	}
	if got := items[0]; got.Kind != "manual" || got.DueTime != "" {
		t.Errorf("adjustment = %+v, want no time", got)
	}
	if got := items[1]; got.Kind != "completion" || got.Title != "Brush teeth" || got.DueTime != "19:30" {
		t.Errorf("completion = %+v, want Brush teeth at 19:30", got)
	}
}
