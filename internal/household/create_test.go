package household

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// On Postgres this runs as a role row-level security applies to.
func TestCreateHousehold(t *testing.T) {
	ctx := t.Context()
	st := store.New(storetest.OpenScoped(t, filepath.Join(t.TempDir(), "ingle.db")))
	svc := New(st)
	svc.SetExternalSignIn(true)

	smiths, mum, err := svc.CreateHousehold(ctx, NewHouseholdInput{
		HouseholdName: " The Smiths ", Timezone: "America/New_York",
		Person: PersonInput{Name: "Mum", Colour: "#0090ff"}, Email: "Mum@Example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if smiths.Name != "The Smiths" || mum.HouseholdID != smiths.ID || mum.Kind != KindAdult ||
		mum.Colour != "#0090FF" || *mum.Email != "mum@example.com" || mum.PasswordHash != nil || mum.PinHash != nil {
		t.Errorf("household %+v, adult %+v", smiths, mum)
	}
	sctx := store.WithHousehold(ctx, smiths.ID)
	set, err := st.Q.GetSettings(sctx, smiths.ID)
	if err != nil || set.Timezone != "America/New_York" {
		t.Errorf("settings = %+v, %v", set, err)
	}

	joneses, dad, err := svc.CreateHousehold(ctx, NewHouseholdInput{
		HouseholdName: "The Joneses", Timezone: "Europe/London",
		Person: PersonInput{Name: "Mum", Colour: "#46A758"}, Email: "dad@example.com",
	})
	if err != nil {
		t.Fatalf("a second household (with its own Mum): %v", err)
	}
	if joneses.ID == smiths.ID {
		t.Fatal("same household twice")
	}
	// Each sees only its own people.
	for _, c := range []struct {
		hh   int64
		want dbq.Person
	}{{smiths.ID, mum}, {joneses.ID, dad}} {
		people, err := svc.People(store.WithHousehold(ctx, c.hh), c.hh)
		if err != nil || len(people) != 1 || people[0].ID != c.want.ID {
			t.Errorf("household %d people = %+v, %v", c.hh, people, err)
		}
	}

	// An email someone has is a field error, and leaves no household.
	before, _ := st.Q.ListHouseholdIDs(store.AsSystem(ctx))
	_, _, err = svc.CreateHousehold(ctx, NewHouseholdInput{
		HouseholdName: "Again", Timezone: "Europe/London",
		Person: PersonInput{Name: "Mum", Colour: "#46A758"}, Email: "MUM@example.com",
	})
	var ve valid.Errors
	if !errors.As(err, &ve) || ve["email"] == "" {
		t.Errorf("duplicate email: %v", err)
	}
	if after, _ := st.Q.ListHouseholdIDs(store.AsSystem(ctx)); len(after) != len(before) {
		t.Errorf("households %v after a failed create, want %v", after, before)
	}
}

func TestCreateHouseholdValidation(t *testing.T) {
	st := store.New(storetest.Open(t, filepath.Join(t.TempDir(), "ingle.db")))
	_, _, err := New(st).CreateHousehold(t.Context(), NewHouseholdInput{Timezone: "Mars/Olympus", Person: PersonInput{Colour: "blue"}, Emoji: "x", Email: "nope"})
	var ve valid.Errors
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	for _, field := range []string{"household_name", "name", "colour", "emoji", "email", "timezone"} {
		if ve[field] == "" {
			t.Errorf("no error for %s: %v", field, ve)
		}
	}
	if ids, _ := st.Q.ListHouseholdIDs(store.AsSystem(t.Context())); len(ids) != 0 {
		t.Errorf("households %v after invalid input", ids)
	}
}
