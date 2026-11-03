package chores

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// boardAt materialises from a week before day and builds the board for day
// (YYYY-MM-DD, local midnight).
func (f *fixture) boardAt(day string) Board {
	f.t.Helper()
	d, err := time.ParseInLocation(DateLayout, day, london)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.Materialise(f.t.Context(), f.hh, d.AddDate(0, 0, -OverdueDays-7), OverdueDays+8); err != nil {
		f.t.Fatal(err)
	}
	b, err := f.svc.Board(f.t.Context(), f.hh, d)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (b Board) column(name string) Column {
	for _, c := range b.Columns {
		if c.Person.Name == name {
			return c
		}
	}
	return Column{}
}

func titles(cards []Card) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Title+"@"+c.DueOn)
	}
	return out
}

func TestBoardOverdueRule(t *testing.T) {
	f := newFixture(t)
	// Board day is Wed 4 Nov; nothing has been done.
	f.chore("Bins", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), startsOn: "2026-10-01"})
	f.chore("Teeth", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=DAILY"), startsOn: "2026-10-01"})
	f.chore("Ancient", choreOpts{assignee: &f.kid.ID, startsOn: "2026-10-01"}) // one-off, > 14 days ago
	f.chore("Recent one-off", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-01"})

	b := f.boardAt("2026-11-04")
	got := titles(b.column("Alfie").Cards)
	// Overdue first (Mon's bins and Sunday's one-off), then today's teeth.
	// Mon/Tue teeth are superseded by today's; the ancient one is too old.
	want := []string{"Recent one-off@2026-11-01", "Bins@2026-11-02", "Teeth@2026-11-04"}
	if !slices.Equal(got, want) {
		t.Fatalf("cards = %v, want %v", got, want)
	}
	for _, c := range b.column("Alfie").Cards[:2] {
		if !c.Overdue {
			t.Errorf("%s not marked overdue", c.Title)
		}
	}
	// Overdue chores count towards "x of y", so "all done" means caught up.
	if col := b.column("Alfie"); col.TotalToday != 3 || col.DoneToday != 0 {
		t.Errorf("progress = %d/%d, want 0/3", col.DoneToday, col.TotalToday)
	}
}

func TestBoardDoneOverdueStaysForTheDay(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins", choreOpts{assignee: &f.kid.ID, rrule: ptr("FREQ=WEEKLY;BYDAY=MO"), startsOn: "2026-10-01"})
	wed := time.Date(2026, 11, 4, 9, 0, 0, 0, london)
	f.svc.now = func() time.Time { return wed }

	b := f.boardAt("2026-11-04")
	cards := b.column("Alfie").Cards
	if len(cards) != 1 || !cards[0].Overdue {
		t.Fatalf("cards = %v, want one overdue", titles(cards))
	}
	if _, err := f.svc.Complete(ctx, f.hh, cards[0].OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	col := f.boardAt("2026-11-04").column("Alfie")
	if len(col.Cards) != 1 || !col.Cards[0].Done || col.Cards[0].Overdue {
		t.Fatalf("cards = %+v, want Bins ticked off and no longer overdue", col.Cards)
	}
	if col.DoneToday != 1 || col.TotalToday != 1 {
		t.Errorf("progress = %d/%d, want 1/1", col.DoneToday, col.TotalToday)
	}
	// The next day it's gone.
	if got := f.boardAt("2026-11-05").column("Alfie").Cards; len(got) != 0 {
		t.Errorf("caught-up chore still shown next day: %v", titles(got))
	}
}

func TestBoardPoolClaimAndDone(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Hoover", choreOpts{startsOn: "2026-11-02", points: 5})
	f.chore("Car", choreOpts{startsOn: "2026-11-02", pence: 200})
	f.chore("Mine", choreOpts{assignee: &f.kid2.ID, startsOn: "2026-11-02", points: 1})

	b := f.boardAt("2026-11-02")
	if got := titles(b.Pool); !slices.Equal(got, []string{"Car@2026-11-02", "Hoover@2026-11-02"}) {
		t.Fatalf("pool = %v", got)
	}
	car, hoover := b.Pool[0], b.Pool[1]

	if err := f.svc.Claim(ctx, f.hh, hoover.OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	// Car is done straight from the pool by Isla, without claiming.
	if _, err := f.svc.Complete(ctx, f.hh, car.OccurrenceID, f.kid2.ID); err != nil {
		t.Fatal(err)
	}

	b = f.boardAt("2026-11-02")
	if len(b.Pool) != 0 {
		t.Errorf("pool = %v, want empty", titles(b.Pool))
	}
	alfie := b.column("Alfie").Cards
	if len(alfie) != 1 || !alfie[0].Claimed || !alfie[0].Pool {
		t.Errorf("Alfie's cards = %+v, want claimed Hoover", alfie)
	}
	isla := b.column("Isla")
	// Undone "Mine" first, then done "Car".
	if got := titles(isla.Cards); !slices.Equal(got, []string{"Mine@2026-11-02", "Car@2026-11-02"}) {
		t.Errorf("Isla's cards = %v", got)
	}
	if !isla.Cards[1].Done || !isla.Cards[1].JustDone || isla.Cards[1].Claimed {
		t.Errorf("Car card = %+v, want done", isla.Cards[1])
	}
	if isla.DoneToday != 1 || isla.TotalToday != 2 {
		t.Errorf("Isla progress = %d/%d, want 1/2", isla.DoneToday, isla.TotalToday)
	}
	if isla.Pence != 200 {
		t.Errorf("Isla pence = %d, want 200", isla.Pence)
	}
}

func TestBoardOrderingAndPending(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	late := "18:00"
	early := "07:30"
	f.chore("Zebra", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02"})
	f.chore("Apple", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02"})
	f.chore("Evening", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02"})
	f.chore("Morning", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02"})
	f.chore("Checked", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 3, approval: true})
	for title, at := range map[string]string{"Evening": late, "Morning": early} {
		if _, err := f.st.DB.ExecContext(ctx, `UPDATE chores SET due_time = ? WHERE title = ?`, at, title); err != nil {
			t.Fatal(err)
		}
	}

	b := f.boardAt("2026-11-02")
	var checked Card
	for _, c := range b.column("Alfie").Cards {
		if c.Title == "Checked" {
			checked = c
		}
	}
	if _, err := f.svc.Complete(ctx, f.hh, checked.OccurrenceID, f.kid.ID); err != nil {
		t.Fatal(err)
	}

	col := f.boardAt("2026-11-02").column("Alfie")
	want := []string{"Morning", "Evening", "Apple", "Zebra", "Checked"}
	var got []string
	for _, c := range col.Cards {
		got = append(got, c.Title)
	}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	last := col.Cards[len(col.Cards)-1]
	if !last.Done || !last.Pending {
		t.Errorf("Checked = %+v, want done and pending", last)
	}
	if col.Points != 0 {
		t.Errorf("points = %d, want 0 while pending", col.Points)
	}
}

func TestBoardSkipsArchivedOwner(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Old job", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02"})
	f.boardAt("2026-11-02")
	if _, err := f.st.DB.ExecContext(ctx, `UPDATE people SET archived_at = 1 WHERE id = ?`, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	b := f.boardAt("2026-11-02")
	if len(b.Columns) != 2 || len(b.Pool) != 0 {
		t.Errorf("columns = %d, pool = %v; want archived person and their chore hidden", len(b.Columns), titles(b.Pool))
	}
}

func TestUndo(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Hoover", choreOpts{startsOn: "2026-11-02", points: 5, pence: 50})
	occ := f.onlyOccurrence()
	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b := f.balance(f.kid); b.Points != 5 {
		t.Fatalf("points = %d, want 5", b.Points)
	}

	base := f.svc.now()
	f.svc.now = func() time.Time { return base.Add(UndoWindow - time.Second) }
	if err := f.svc.Undo(ctx, f.hh, c.ID); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if b := f.balance(f.kid); b.Points != 0 || b.Pence != 0 {
		t.Errorf("balance = %+v, want zero after undo", b)
	}
	got := f.onlyOccurrence()
	if got.Status != StatusClaimed || got.ClaimedBy == nil || *got.ClaimedBy != f.kid.ID {
		t.Errorf("occurrence = %+v, want claimed by Alfie again", got)
	}
	if err := f.svc.Undo(ctx, f.hh, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Undo = %v, want ErrNotFound", err)
	}

	// Complete again and wait too long.
	c, err = f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.now = func() time.Time { return base.Add(2 * UndoWindow) }
	if err := f.svc.Undo(ctx, f.hh, c.ID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("late Undo = %v, want ErrUndoExpired", err)
	}
}

func TestUndoAfterReview(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Checked", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 3, approval: true})
	occ := f.onlyOccurrence()
	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Approve(ctx, f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Undo(ctx, f.hh, c.ID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("Undo after approval = %v, want ErrUndoExpired", err)
	}
	if b := f.balance(f.kid); b.Points != 3 {
		t.Errorf("points = %d, want 3 kept", b.Points)
	}
}

func TestUndoPendingReopens(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Checked", choreOpts{assignee: &f.kid.ID, startsOn: "2026-11-02", points: 3, approval: true})
	occ := f.onlyOccurrence()
	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Undo(ctx, f.hh, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.onlyOccurrence(); got.Status != StatusOpen {
		t.Errorf("status = %s, want open", got.Status)
	}
}
