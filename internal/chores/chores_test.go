package chores

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

var london = mustLoad("Europe/London")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

type fixture struct {
	t     *testing.T
	st    *store.Store
	svc   *Service
	hh    int64
	adult dbq.Person
	kid   dbq.Person
	kid2  dbq.Person
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "domus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.New(db)
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, london)
	h, err := st.EnsureHousehold(ctx, "Home", now)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, FixedLocation(london))
	svc.now = func() time.Time { return now }

	f := &fixture{t: t, st: st, svc: svc, hh: h.ID}
	f.adult = f.person("Mum", "adult")
	f.kid = f.person("Alfie", "child")
	f.kid2 = f.person("Isla", "child")
	return f
}

func (f *fixture) person(name, kind string) dbq.Person {
	f.t.Helper()
	p, err := f.st.Q.CreatePerson(f.t.Context(), dbq.CreatePersonParams{
		HouseholdID: f.hh, Name: name, Kind: kind, Colour: "#4A90D9", CreatedAt: 1,
	})
	if err != nil {
		f.t.Fatalf("CreatePerson: %v", err)
	}
	return p
}

type choreOpts struct {
	assignee *int64
	rrule    *string
	startsOn string
	points   int64
	pence    int64
	approval bool
	anyDay   bool // a flexible-day chore
}

func (f *fixture) chore(title string, o choreOpts) dbq.Chore {
	f.t.Helper()
	mode := ModePool
	if o.assignee != nil {
		mode = ModeAssigned
	}
	if o.startsOn == "" {
		o.startsOn = "2026-11-02"
	}
	var approval int64
	if o.approval {
		approval = 1
	}
	c, err := f.st.Q.CreateChore(f.t.Context(), dbq.CreateChoreParams{
		HouseholdID: f.hh, Title: title, Mode: mode, AssigneeID: o.assignee, Rrule: o.rrule,
		StartsOn: o.startsOn, Points: o.points, Pence: o.pence, RequiresApproval: approval,
		CreatedAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		f.t.Fatalf("CreateChore: %v", err)
	}
	if o.anyDay {
		if c, err = f.st.Q.SetChoreDayMode(f.t.Context(), dbq.SetChoreDayModeParams{DayMode: DayAny, HouseholdID: f.hh, ID: c.ID}); err != nil {
			f.t.Fatalf("SetChoreDayMode: %v", err)
		}
	}
	return c
}

func (f *fixture) board(from, to string) []dbq.ListBoardRow {
	f.t.Helper()
	rows, err := f.st.Q.ListBoard(f.t.Context(), dbq.ListBoardParams{HouseholdID: f.hh, FromDate: from, ToDate: to})
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// onlyOccurrence materialises today and returns the single occurrence.
func (f *fixture) onlyOccurrence() dbq.ListBoardRow {
	f.t.Helper()
	if _, err := f.svc.Materialise(f.t.Context(), f.hh, f.svc.now(), 1); err != nil {
		f.t.Fatal(err)
	}
	rows := f.board("2026-11-02", "2026-11-02")
	if len(rows) != 1 {
		f.t.Fatalf("got %d occurrences, want 1", len(rows))
	}
	return rows[0]
}

func (f *fixture) balance(p dbq.Person) dbq.BalanceForPersonRow {
	f.t.Helper()
	b, err := f.st.Q.BalanceForPerson(f.t.Context(), dbq.BalanceForPersonParams{HouseholdID: f.hh, PersonID: p.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func dueDates(rows []dbq.ListBoardRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.DueOn)
	}
	return out
}

func TestMaterialiseWeekly(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Bins", choreOpts{rrule: ptr("FREQ=WEEKLY;BYDAY=MO,TH")})

	n, err := f.svc.Materialise(ctx, f.hh, f.svc.now(), 14)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-11-02", "2026-11-05", "2026-11-09", "2026-11-12"}
	if n != len(want) {
		t.Errorf("created %d, want %d", n, len(want))
	}
	if got := dueDates(f.board("2026-11-01", "2026-11-30")); !slices.Equal(got, want) {
		t.Errorf("dates = %v, want %v", got, want)
	}

	n, err = f.svc.Materialise(ctx, f.hh, f.svc.now(), 14)
	if err != nil || n != 0 {
		t.Errorf("second run created %d (err %v), want 0", n, err)
	}
}

func TestMaterialiseAcrossClocksGoingBack(t *testing.T) {
	f := newFixture(t)
	f.chore("Feed the cat", choreOpts{rrule: ptr("FREQ=DAILY"), startsOn: "2026-10-20"})

	from := time.Date(2026, 10, 24, 12, 0, 0, 0, london)
	if _, err := f.svc.Materialise(t.Context(), f.hh, from, 4); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27"}
	if got := dueDates(f.board("2026-10-01", "2026-10-31")); !slices.Equal(got, want) {
		t.Errorf("dates = %v, want %v", got, want)
	}
}

func TestMaterialiseOneOffAndArchived(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Past one-off", choreOpts{startsOn: "2026-10-30"})
	f.chore("Far future", choreOpts{startsOn: "2026-12-25"})
	archived := f.chore("Archived", choreOpts{rrule: ptr("FREQ=DAILY")})
	if err := f.st.Q.ArchiveChore(ctx, dbq.ArchiveChoreParams{ArchivedAt: ptr(int64(1)), UpdatedAt: 1, HouseholdID: f.hh, ID: archived.ID}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.Materialise(ctx, f.hh, f.svc.now(), 14); err != nil {
		t.Fatal(err)
	}
	if got := dueDates(f.board("2026-01-01", "2026-12-31")); !slices.Equal(got, []string{"2026-10-30"}) {
		t.Errorf("dates = %v, want only the past one-off", got)
	}
}

func TestValidateRRule(t *testing.T) {
	if err := ValidateRRule("FREQ=WEEKLY;BYDAY=SA"); err != nil {
		t.Errorf("valid rule rejected: %v", err)
	}
	if err := ValidateRRule("FREQ=FORTNIGHTLY"); !errors.Is(err, ErrInvalidRRule) {
		t.Errorf("err = %v, want ErrInvalidRRule", err)
	}
}

func TestClaimAndRelease(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Hoover lounge", choreOpts{points: 5})
	occ := f.onlyOccurrence()

	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrAlreadyClaimed) {
		t.Errorf("second claim err = %v, want ErrAlreadyClaimed", err)
	}
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("complete by non-claimant err = %v, want ErrNotYours", err)
	}
	if err := f.svc.Release(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrNotClaimedByYou) {
		t.Errorf("release by other err = %v, want ErrNotClaimedByYou", err)
	}
	if err := f.svc.Release(ctx, f.hh, occ.ID, f.kid.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid2.ID); err != nil {
		t.Errorf("claim after release: %v", err)
	}
	if err := f.svc.Claim(ctx, f.hh, 9999, f.kid.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing occurrence err = %v, want ErrNotFound", err)
	}
}

func TestAssignedNotClaimable(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Make bed", choreOpts{assignee: &f.kid.ID, points: 2})
	occ := f.onlyOccurrence()

	if err := f.svc.Claim(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrNotClaimable) {
		t.Errorf("err = %v, want ErrNotClaimable", err)
	}
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid2.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("err = %v, want ErrNotYours", err)
	}
}

func TestCompleteWithoutApproval(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Make bed", choreOpts{assignee: &f.kid.ID, points: 2, pence: 50})
	occ := f.onlyOccurrence()

	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusApproved {
		t.Errorf("status = %q, want approved", c.Status)
	}
	if b := f.balance(f.kid); b.Points != 2 || b.Pence != 50 {
		t.Errorf("balance = %+v, want 2 points / 50p", b)
	}
	if _, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("double complete err = %v, want ErrNotOpen", err)
	}
}

func TestApprovalFlow(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.chore("Wash car", choreOpts{points: 10, pence: 200, approval: true})
	occ := f.onlyOccurrence()

	c, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusPending {
		t.Fatalf("status = %q, want pending", c.Status)
	}
	if b := f.balance(f.kid); b.Points != 0 || b.Pence != 0 {
		t.Errorf("pending completion credited: %+v", b)
	}
	if err := f.svc.Approve(ctx, f.hh, c.ID, f.kid2.ID); !errors.Is(err, ErrNotAdult) {
		t.Errorf("child approve err = %v, want ErrNotAdult", err)
	}

	// Reject: no credit, occurrence reopens and can be completed again.
	if err := f.svc.Reject(ctx, f.hh, c.ID, f.adult.ID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if b := f.balance(f.kid); b.Points != 0 {
		t.Errorf("rejected completion credited: %+v", b)
	}
	if err := f.svc.Approve(ctx, f.hh, c.ID, f.adult.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("approve rejected err = %v, want ErrNotPending", err)
	}

	c2, err := f.svc.Complete(ctx, f.hh, occ.ID, f.kid.ID)
	if err != nil {
		t.Fatalf("redo after reject: %v", err)
	}
	if err := f.svc.Approve(ctx, f.hh, c2.ID, f.adult.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if b := f.balance(f.kid); b.Points != 10 || b.Pence != 200 {
		t.Errorf("balance = %+v, want 10 points / £2.00", b)
	}

	acts, err := f.st.Q.ListRecentActivity(ctx, dbq.ListRecentActivityParams{HouseholdID: f.hh, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, a := range acts {
		verbs = append(verbs, a.Verb)
	}
	want := []string{"chore.approved", "chore.completed", "chore.rejected", "chore.completed"}
	if !slices.Equal(verbs, want) {
		t.Errorf("activity = %v, want %v", verbs, want)
	}
}

func TestCrossHouseholdPersonRejected(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	other, err := f.st.Q.CreateHousehold(ctx, dbq.CreateHouseholdParams{Name: "Next door", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.chore("Hoover", choreOpts{})
	occ := f.onlyOccurrence()

	if err := f.svc.Claim(ctx, other.ID, occ.ID, f.kid.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func dbqActivityParams(hh int64) dbq.ListRecentActivityParams {
	return dbq.ListRecentActivityParams{HouseholdID: hh, Limit: 50}
}
