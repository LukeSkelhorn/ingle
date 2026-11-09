// Package module lets each household switch whole features on or off
// (Admin › Modules). A module brings its own routes, admin nav links and
// background loops; when it is off its routes return 404, its links and
// board pieces are hidden and its loops skip the household. Its data is
// kept, so switching it back on brings everything back.
//
// Modules are compiled in and registered at startup in a fixed order;
// there is no loading of outside code.
package module

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// Module is one feature that can be switched on or off.
type Module interface {
	// ID is stable and stored in the database (e.g. "calendar").
	ID() string
	// Name and Description are shown in Admin › Modules.
	Name() string
	Description() string
	// Requires lists the IDs of modules this one needs. They must be
	// registered before it.
	Requires() []string
	// DefaultOn is whether a new household starts with it on.
	DefaultOn() bool
	// Routes registers the module's pages. The router hides them when the
	// module is off.
	Routes(r Router)
	// AdminNav is the module's links in the admin bar.
	AdminNav() []NavItem
	// Loops are the module's background jobs.
	Loops() []Loop
}

// Router registers a module's routes behind the server's usual guards.
type Router interface {
	// Viewer is a page for a household display or a signed-in adult.
	Viewer(pattern string, h http.HandlerFunc)
	// Admin is a page for a signed-in adult.
	Admin(pattern string, h http.HandlerFunc)
}

// NavItem is a link in the admin bar. Key matches the page's view.Nav.
type NavItem struct {
	Key, Label, Path string
}

// Loop is a background job, run for each household that has the module on.
type Loop struct {
	Name string
	// Every is the time between runs. The loop also runs once at startup.
	Every time.Duration
	Run   func(ctx context.Context, householdID int64) error
}

// Registry holds the compiled-in modules in registration order.
type Registry struct {
	mods []Module
	byID map[string]Module
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{byID: map[string]Module{}} }

// Register adds m. It panics on a duplicate ID or a requirement that isn't
// registered yet; registering requirements first also rules out cycles.
func (r *Registry) Register(m Module) {
	id := m.ID()
	if id == "" {
		panic("module: empty ID")
	}
	if _, dup := r.byID[id]; dup {
		panic(fmt.Sprintf("module: %q registered twice", id))
	}
	for _, req := range m.Requires() {
		if _, ok := r.byID[req]; !ok {
			panic(fmt.Sprintf("module: %q requires %q, which isn't registered before it", id, req))
		}
	}
	r.mods = append(r.mods, m)
	r.byID[id] = m
}

// All returns the modules in registration order.
func (r *Registry) All() []Module { return slices.Clone(r.mods) }

// Get returns the module with id.
func (r *Registry) Get(id string) (Module, bool) {
	m, ok := r.byID[id]
	return m, ok
}

// Defaults is each module's DefaultOn.
func (r *Registry) Defaults() map[string]bool {
	d := make(map[string]bool, len(r.mods))
	for _, m := range r.mods {
		d[m.ID()] = m.DefaultOn()
	}
	return d
}

// Needs returns every module id needs, directly or not, in registration
// order.
func (r *Registry) Needs(id string) []string {
	need := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		m, ok := r.byID[id]
		if !ok {
			return
		}
		for _, req := range m.Requires() {
			if !need[req] {
				need[req] = true
				walk(req)
			}
		}
	}
	walk(id)
	return r.ordered(need)
}

// NeededBy returns every module that needs id, directly or not, in
// registration order.
func (r *Registry) NeededBy(id string) []string {
	by := map[string]bool{}
	// Requirements come first, so one pass in order catches chains.
	for _, m := range r.mods {
		for _, req := range m.Requires() {
			if req == id || by[req] {
				by[m.ID()] = true
				break
			}
		}
	}
	return r.ordered(by)
}

func (r *Registry) ordered(set map[string]bool) []string {
	var out []string
	for _, m := range r.mods {
		if set[m.ID()] {
			out = append(out, m.ID())
		}
	}
	return out
}

// Change is the outcome of asking for a set of modules.
type Change struct {
	// Final is every module's new state.
	Final map[string]bool
	// AlsoOn were switched on because a module asked for needs them.
	AlsoOn []string
	// AlsoOff were switched off because a module they need was turned off.
	AlsoOff []string
}

// Plan works out the change from current to want, keeping requirements
// consistent: turning a module on turns on what it needs, and turning one
// off turns off what needs it. Modules missing from want keep their current
// state.
func (r *Registry) Plan(current, want map[string]bool) Change {
	final := make(map[string]bool, len(r.mods))
	for _, m := range r.mods {
		id := m.ID()
		v, ok := want[id]
		if !ok {
			v = current[id]
		}
		final[id] = v
	}
	c := Change{Final: final}
	for _, m := range r.mods {
		id := m.ID()
		if !final[id] || current[id] {
			continue
		}
		for _, req := range r.Needs(id) {
			if !final[req] {
				final[req] = true
				c.AlsoOn = append(c.AlsoOn, req)
			}
		}
	}
	for _, m := range r.mods {
		id := m.ID()
		if final[id] || !current[id] {
			continue
		}
		for _, dep := range r.NeededBy(id) {
			if final[dep] {
				final[dep] = false
				if want[dep] || !hasKey(want, dep) {
					c.AlsoOff = append(c.AlsoOff, dep)
				}
			}
		}
	}
	// Anything still on without what it needs (only possible when current
	// itself was inconsistent) goes off.
	for _, m := range r.mods {
		for _, req := range m.Requires() {
			if final[m.ID()] && !final[req] {
				final[m.ID()] = false
			}
		}
	}
	return c
}

func hasKey(m map[string]bool, k string) bool {
	_, ok := m[k]
	return ok
}
