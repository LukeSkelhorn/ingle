package household

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
	"github.com/lukeskelhorn/domus/internal/valid"
)

type fixture struct {
	t   *testing.T
	st  *store.Store
	svc *Service
	hh  int64
	mum dbq.Person
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "domus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	h, err := st.EnsureHousehold(ctx, "Home", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, svc: New(st), hh: h.ID}
	need, err := f.svc.NeedsSetup(ctx, h.ID)
	if err != nil || !need {
		t.Fatalf("NeedsSetup = %v, %v", need, err)
	}
	f.mum, err = f.svc.Setup(ctx, h.ID, SetupInput{
		HouseholdName: "Smiths",
		Person:        PersonInput{Name: "Mum", Colour: "#0090ff"},
		Email:         "Mum@Example.com",
		Password:      "correct horse battery",
		PIN:           "2468",
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) add(name, kind string) dbq.Person {
	f.t.Helper()
	p, err := f.svc.CreatePerson(f.t.Context(), f.hh, f.mum.ID, PersonInput{Name: name, Kind: kind, Colour: "#46A758"})
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestSetup(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if f.mum.Kind != KindAdult || f.mum.Colour != "#0090FF" || *f.mum.Email != "mum@example.com" ||
		!auth.VerifyPIN(*f.mum.PinHash, "2468") || !auth.VerifyPassword(*f.mum.PasswordHash, "correct horse battery") {
		t.Errorf("setup person = %+v", f.mum)
	}
	h, _ := f.st.Q.GetHousehold(ctx, f.hh)
	if h.Name != "Smiths" {
		t.Errorf("household name %q", h.Name)
	}
	if need, _ := f.svc.NeedsSetup(ctx, f.hh); need {
		t.Error("still needs setup")
	}
	_, err := f.svc.Setup(ctx, f.hh, SetupInput{
		HouseholdName: "Again", Person: PersonInput{Name: "Dad", Colour: "#0090FF"},
		Email: "dad@example.com", Password: "correct horse battery", PIN: "1357",
	})
	if !errors.Is(err, ErrSetupDone) {
		t.Errorf("second setup: %v", err)
	}
}

func TestSetupValidation(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "domus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	h, _ := st.EnsureHousehold(ctx, "Home", time.Now())
	_, err = New(st).Setup(ctx, h.ID, SetupInput{Person: PersonInput{Colour: "blue"}, Email: "nope", Password: "short", PIN: "12a4"})
	var ve valid.Errors
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	for _, field := range []string{"household_name", "name", "colour", "email", "password", "pin"} {
		if ve[field] == "" {
			t.Errorf("no error for %s: %v", field, ve)
		}
	}
}

func TestArchiveRules(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	kid := f.add("Alfie", KindChild)

	if _, err := f.st.Q.CreateChore(ctx, dbq.CreateChoreParams{
		HouseholdID: f.hh, Title: "Bins", Mode: "assigned", AssigneeID: &kid.ID, StartsOn: "2026-11-02", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ArchivePerson(ctx, f.hh, f.mum.ID, kid.ID); !errors.Is(err, ErrHasAssignedChores) {
		t.Errorf("archive with chores: %v", err)
	}
	if err := f.svc.ArchivePerson(ctx, f.hh, f.mum.ID, f.mum.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("archive last admin: %v", err)
	}
	if err := f.svc.ClearPIN(ctx, f.hh, f.mum.ID, f.mum.ID, nil); err != nil {
		t.Errorf("clear PIN with password still set: %v", err)
	}
	if err := f.svc.ClearLogin(ctx, f.hh, f.mum.ID, f.mum.ID, nil); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("clear last credential: %v", err)
	}
	if _, err := f.svc.UpdatePerson(ctx, f.hh, f.mum.ID, f.mum.ID, PersonInput{Name: "Mum", Kind: KindChild, Colour: "#0090FF"}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demote last admin: %v", err)
	}

	// With a second adult who can sign in, Mum can be archived and restored.
	dad := f.add("Dad", KindAdult)
	if err := f.svc.SetPIN(ctx, f.hh, f.mum.ID, dad.ID, "1357", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ArchivePerson(ctx, f.hh, dad.ID, f.mum.ID); err != nil {
		t.Fatalf("archive Mum: %v", err)
	}
	archived, _ := f.svc.ArchivedPeople(ctx, f.hh)
	if len(archived) != 1 || archived[0].ID != f.mum.ID {
		t.Errorf("archived = %v", archived)
	}
	if err := f.svc.RestorePerson(ctx, f.hh, dad.ID, f.mum.ID); err != nil {
		t.Fatal(err)
	}
	if people, _ := f.svc.People(ctx, f.hh); len(people) != 3 {
		t.Errorf("people after restore = %d", len(people))
	}
}

func TestCredentialsAdultOnly(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	kid := f.add("Alfie", KindChild)
	if err := f.svc.SetPIN(ctx, f.hh, f.mum.ID, kid.ID, "1234", nil); !errors.Is(err, ErrNotAdult) {
		t.Errorf("kid PIN: %v", err)
	}
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, kid.ID, "kid@example.com", "a long password", nil); !errors.Is(err, ErrNotAdult) {
		t.Errorf("kid login: %v", err)
	}

	// Demoting an adult (not the last) strips their credentials.
	dad := f.add("Dad", KindAdult)
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, dad.ID, "dad@example.com", "", nil); err == nil {
		t.Error("login without a password accepted")
	}
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, dad.ID, "MUM@example.com", "a long password", nil); err == nil {
		t.Error("duplicate email accepted")
	}
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, dad.ID, "dad@example.com", "a long password", nil); err != nil {
		t.Fatal(err)
	}
	// Email-only change keeps the password.
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, dad.ID, "dad2@example.com", "", nil); err != nil {
		t.Fatal(err)
	}
	dad, _ = f.svc.Person(ctx, f.hh, dad.ID)
	if *dad.Email != "dad2@example.com" || !auth.VerifyPassword(*dad.PasswordHash, "a long password") {
		t.Errorf("dad = %+v", dad)
	}
	if _, err := f.svc.UpdatePerson(ctx, f.hh, f.mum.ID, dad.ID, PersonInput{Name: "Dad", Kind: KindChild, Colour: "#0090FF"}); err != nil {
		t.Fatal(err)
	}
	dad, _ = f.svc.Person(ctx, f.hh, dad.ID)
	if dad.Email != nil || dad.PasswordHash != nil || dad.PinHash != nil {
		t.Errorf("demoted dad keeps credentials: %+v", dad)
	}
}

func TestCredentialChangeEndsOtherSessions(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	mk := func(token string) {
		if err := f.st.Q.CreateSession(ctx, dbq.CreateSessionParams{
			TokenHash: auth.HashToken(token), HouseholdID: f.hh, PersonID: f.mum.ID, Kind: "personal", CreatedAt: 1, LastSeenAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("mine")
	mk("old-phone")
	if err := f.svc.SetLogin(ctx, f.hh, f.mum.ID, f.mum.ID, "mum@example.com", "a new long password", auth.HashToken("mine")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Q.GetSessionByToken(ctx, auth.HashToken("mine")); err != nil {
		t.Errorf("own session ended: %v", err)
	}
	if _, err := f.st.Q.GetSessionByToken(ctx, auth.HashToken("old-phone")); err == nil {
		t.Error("other session survived a password change")
	}
}

func TestRewards(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Free"}); err == nil {
		t.Error("free reward accepted")
	}
	r, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: " Comic ", CostPence: 250})
	if err != nil || r.Title != "Comic" {
		t.Fatalf("CreateReward = %+v, %v", r, err)
	}
	r, err = f.svc.UpdateReward(ctx, f.hh, f.mum.ID, r.ID, RewardInput{Title: "Comic book", CostPoints: 20, CostPence: 300})
	if err != nil || r.CostPoints != 20 || r.CostPence != 300 {
		t.Fatalf("UpdateReward = %+v, %v", r, err)
	}
	if err := f.svc.ArchiveReward(ctx, f.hh, f.mum.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.svc.Rewards(ctx, f.hh); len(list) != 0 {
		t.Errorf("active rewards after archive = %v", list)
	}
	if err := f.svc.RestoreReward(ctx, f.hh, f.mum.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateReward(ctx, f.hh, f.mum.ID, 9999, RewardInput{Title: "x", CostPoints: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing reward: %v", err)
	}
}

func TestRewardEmojiAndGoals(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Lego", Emoji: "x", CostPoints: 1}); err == nil {
		t.Error("emoji outside the picker accepted")
	}
	r, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Lego", Emoji: "🧱", CostPoints: 100})
	if err != nil || r.Emoji != "🧱" {
		t.Fatalf("CreateReward = %+v, %v", r, err)
	}
	if r, err = f.svc.UpdateReward(ctx, f.hh, f.mum.ID, r.ID, RewardInput{Title: "Lego", CostPoints: 100}); err != nil || r.Emoji != "" {
		t.Fatalf("UpdateReward = %+v, %v", r, err)
	}

	// Archiving a reward clears anyone saving for it; restoring doesn't
	// bring the goal back.
	kid := f.add("Alfie", KindChild)
	at := int64(1)
	if _, err := f.st.Q.SetGoal(ctx, dbq.SetGoalParams{RewardID: &r.ID, SetAt: &at, HouseholdID: f.hh, ID: kid.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ArchiveReward(ctx, f.hh, f.mum.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RestoreReward(ctx, f.hh, f.mum.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.Person(ctx, f.hh, kid.ID); p.GoalRewardID != nil || p.GoalSetAt != nil {
		t.Errorf("goal after archive = %v, %v", p.GoalRewardID, p.GoalSetAt)
	}
}

func TestTypedRewardEmojiAndPictureCards(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	r, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Dino park", Emoji: "🦕", CostPoints: 10})
	if err != nil || r.Emoji != "🦕" {
		t.Fatalf("typed emoji = %+v, %v", r, err)
	}
	if _, err := f.svc.CreateReward(ctx, f.hh, f.mum.ID, RewardInput{Title: "Two", Emoji: "🦕🦖", CostPoints: 10}); err == nil {
		t.Error("two emoji accepted")
	}

	kid, err := f.svc.CreatePerson(ctx, f.hh, f.mum.ID, PersonInput{Name: "Alfie", Kind: KindChild, Colour: "#46A758", PictureCards: true})
	if err != nil || kid.PictureCards != 1 {
		t.Fatalf("child with picture cards = %d, %v", kid.PictureCards, err)
	}
	kid, err = f.svc.UpdatePerson(ctx, f.hh, f.mum.ID, kid.ID, PersonInput{Name: "Alfie", Kind: KindChild, Colour: "#46A758"})
	if err != nil || kid.PictureCards != 0 {
		t.Fatalf("turned off = %d, %v", kid.PictureCards, err)
	}
	// Only children have them.
	dad, err := f.svc.CreatePerson(ctx, f.hh, f.mum.ID, PersonInput{Name: "Dad", Kind: KindAdult, Colour: "#46A758", PictureCards: true})
	if err != nil || dad.PictureCards != 0 {
		t.Fatalf("adult with picture cards = %d, %v", dad.PictureCards, err)
	}
}

func TestArchiveLeavesSharedChores(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	alfie, isla, freddie := f.add("Alfie", KindChild), f.add("Isla", KindChild), f.add("Freddie", KindChild)

	shared := func(title string, people ...int64) dbq.Chore {
		t.Helper()
		c, err := f.st.Q.CreateChore(ctx, dbq.CreateChoreParams{
			HouseholdID: f.hh, Title: title, Mode: "shared", StartsOn: "2026-11-02", CreatedAt: 1, UpdatedAt: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range people {
			if err := f.st.Q.AddChorePerson(ctx, dbq.AddChorePersonParams{HouseholdID: f.hh, ChoreID: c.ID, PersonID: p}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.st.Q.InsertOccurrence(ctx, dbq.InsertOccurrenceParams{HouseholdID: f.hh, ChoreID: c.ID, DueOn: "2026-11-02"}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	three := shared("Feed the cat", alfie.ID, isla.ID, freddie.ID)
	two := shared("Tidy the den", alfie.ID, isla.ID)

	// Shared chores don't block archiving Alfie; he's taken out of both.
	if err := f.svc.ArchivePerson(ctx, f.hh, f.mum.ID, alfie.ID); err != nil {
		t.Fatalf("archive Alfie: %v", err)
	}
	if got, _ := f.st.Q.ListPeopleForChore(ctx, dbq.ListPeopleForChoreParams{HouseholdID: f.hh, ChoreID: three.ID}); len(got) != 2 {
		t.Errorf("three-person chore now has %v", got)
	}
	c, _ := f.st.Q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: f.hh, ID: three.ID})
	if c.Mode != "shared" {
		t.Errorf("three-person chore mode = %q", c.Mode)
	}

	// The two-person one is Isla's now, today's occurrence included.
	c, _ = f.st.Q.GetChore(ctx, dbq.GetChoreParams{HouseholdID: f.hh, ID: two.ID})
	if c.Mode != "assigned" || c.AssigneeID == nil || *c.AssigneeID != isla.ID {
		t.Errorf("two-person chore = %+v, want assigned to Isla", c)
	}
	if got, _ := f.st.Q.ListPeopleForChore(ctx, dbq.ListPeopleForChoreParams{HouseholdID: f.hh, ChoreID: two.ID}); len(got) != 0 {
		t.Errorf("assigned chore still has people %v", got)
	}
	rows, err := f.st.Q.ListBoard(ctx, dbq.ListBoardParams{HouseholdID: f.hh, FromDate: "2026-11-02", ToDate: "2026-11-02"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ChoreID == two.ID && (r.AssigneeID == nil || *r.AssigneeID != isla.ID) {
			t.Errorf("occurrence assignee = %v, want Isla", r.AssigneeID)
		}
	}
	// Now it's assigned, it blocks archiving Isla like any other.
	if err := f.svc.ArchivePerson(ctx, f.hh, f.mum.ID, isla.ID); !errors.Is(err, ErrHasAssignedChores) {
		t.Errorf("archive Isla = %v, want ErrHasAssignedChores", err)
	}
}
