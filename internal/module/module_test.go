package module

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
)

type fake struct {
	id       string
	requires []string
	on       bool
}

func (f fake) ID() string          { return f.id }
func (f fake) Name() string        { return f.id }
func (f fake) Description() string { return "" }
func (f fake) Requires() []string  { return f.requires }
func (f fake) DefaultOn() bool     { return f.on }
func (f fake) Routes(Router)       {}
func (f fake) AdminNav() []NavItem { return nil }
func (f fake) Loops() []Loop       { return nil }

func newFake(id string, on bool, requires ...string) fake {
	return fake{id: id, on: on, requires: requires}
}

// reg is rewards ← money ← bank, and photos on its own.
func reg() *Registry {
	r := NewRegistry()
	r.Register(newFake("rewards", true))
	r.Register(newFake("photos", true))
	r.Register(newFake("money", false, "rewards"))
	r.Register(newFake("bank", false, "money"))
	return r
}

func TestRegisterPanics(t *testing.T) {
	for name, f := range map[string]func(r *Registry){
		"duplicate":            func(r *Registry) { r.Register(newFake("rewards", true)) },
		"unknown requirement":  func(r *Registry) { r.Register(newFake("x", true, "nope")) },
		"empty ID":             func(r *Registry) { r.Register(newFake("", true)) },
		"requirement too late": func(r *Registry) { r.Register(newFake("y", true, "z")); r.Register(newFake("z", true)) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			f(reg())
		})
	}
}

func TestNeeds(t *testing.T) {
	r := reg()
	if got := r.Needs("bank"); !slices.Equal(got, []string{"rewards", "money"}) {
		t.Errorf("Needs(bank) = %v", got)
	}
	if got := r.NeededBy("rewards"); !slices.Equal(got, []string{"money", "bank"}) {
		t.Errorf("NeededBy(rewards) = %v", got)
	}
	if got := r.NeededBy("photos"); got != nil {
		t.Errorf("NeededBy(photos) = %v", got)
	}
	if got := r.All(); len(got) != 4 || got[0].ID() != "rewards" || got[3].ID() != "bank" {
		t.Errorf("All = %v", got)
	}
}

func TestPlan(t *testing.T) {
	r := reg()
	all := map[string]bool{"rewards": true, "photos": true, "money": true, "bank": true}

	// Turning rewards off takes money and bank with it.
	c := r.Plan(all, map[string]bool{"rewards": false, "photos": true, "money": true, "bank": true})
	if c.Final["rewards"] || c.Final["money"] || c.Final["bank"] || !c.Final["photos"] {
		t.Errorf("Final = %v", c.Final)
	}
	if !slices.Equal(c.AlsoOff, []string{"money", "bank"}) {
		t.Errorf("AlsoOff = %v", c.AlsoOff)
	}

	// Unticking them too needs no warning.
	c = r.Plan(all, map[string]bool{"rewards": false, "photos": true, "money": false, "bank": false})
	if len(c.AlsoOff) != 0 {
		t.Errorf("AlsoOff = %v", c.AlsoOff)
	}

	// Turning bank on turns on what it needs.
	none := map[string]bool{"photos": true}
	c = r.Plan(none, map[string]bool{"photos": true, "bank": true})
	if !c.Final["bank"] || !c.Final["money"] || !c.Final["rewards"] {
		t.Errorf("Final = %v", c.Final)
	}
	if !slices.Equal(c.AlsoOn, []string{"rewards", "money"}) {
		t.Errorf("AlsoOn = %v", c.AlsoOn)
	}

	// Missing from want: unchanged.
	c = r.Plan(all, map[string]bool{"photos": false})
	if c.Final["photos"] || !c.Final["bank"] {
		t.Errorf("Final = %v", c.Final)
	}

	// An inconsistent current state is put right.
	c = r.Plan(map[string]bool{"money": true}, map[string]bool{"money": true})
	if c.Final["money"] {
		t.Errorf("money on without rewards: %v", c.Final)
	}
}

func TestService(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	hh, err := st.EnsureHousehold(ctx, "Home", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	r := reg()
	svc := NewService(st, r)
	changes := 0
	svc.OnChange(func(int64) { changes++ })

	// Not chosen yet: everything is on.
	s, err := svc.State(ctx, hh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Chosen || !s.On["bank"] || !s.On["photos"] {
		t.Errorf("before choosing: %+v", s)
	}
	if svc.Enabled(ctx, hh.ID, "nope") {
		t.Error("unknown module is on")
	}

	// Defaults: rewards and photos.
	if err := svc.SetDefaults(ctx, hh.ID, nil); err != nil {
		t.Fatal(err)
	}
	if !svc.Enabled(ctx, hh.ID, "rewards") || svc.Enabled(ctx, hh.ID, "money") {
		t.Error("defaults not applied")
	}
	if s, _ := svc.State(ctx, hh.ID); !s.Chosen {
		t.Error("not chosen after SetDefaults")
	}

	// Set clears the cache and tells listeners; the activity is logged.
	if err := svc.Set(ctx, hh.ID, nil, map[string]bool{"rewards": false, "photos": true}); err != nil {
		t.Fatal(err)
	}
	if svc.Enabled(ctx, hh.ID, "rewards") || changes != 2 {
		t.Errorf("rewards on after Set, changes = %d", changes)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM activity WHERE verb = 'modules.updated'").Scan(&n); err != nil || n != 2 {
		t.Errorf("activity rows = %d, %v", n, err)
	}

	// A module added in a later version gets its default.
	r.Register(newFake("later", true))
	svc.Invalidate(hh.ID)
	if !svc.Enabled(ctx, hh.ID, "later") {
		t.Error("new module isn't on by default")
	}

	// Data is kept: the rows stay, switching back on restores.
	if err := svc.Set(ctx, hh.ID, nil, map[string]bool{"rewards": true, "photos": true, "later": true}); err != nil {
		t.Fatal(err)
	}
	if !svc.Enabled(ctx, hh.ID, "rewards") {
		t.Error("rewards still off")
	}
}
