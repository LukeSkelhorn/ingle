// Package module lets each household switch whole features on or off (Admin › Modules).
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
	ID() string
	Name() string
	Description() string
	Requires() []string
	DefaultOn() bool
	Routes(r Router)
	AdminNav() []NavItem
	Loops() []Loop
}

// Router registers a module's routes behind the server's usual guards.
type Router interface {
	Viewer(pattern string, h http.HandlerFunc)
	Admin(pattern string, h http.HandlerFunc)
}

// NavItem is a link in the admin bar.
type NavItem struct {
	Key, Label, Path string
	InSettings       bool
}

// Loop is a background job, run for each household that has the module on.
type Loop struct {
	Name  string
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

// Register adds m.
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

// Needs returns every module id needs, directly or not, in registration order.
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

// NeededBy returns every module that needs id, directly or not, in registration order.
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
	Final   map[string]bool
	AlsoOn  []string
	AlsoOff []string
}

// Plan works out the change from current to want, keeping requirements consistent.
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
