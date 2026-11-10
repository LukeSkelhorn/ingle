package server

import (
	"net/http"
	"slices"
	"strings"
)

// Admin › Modules: switch whole features on or off.

type modulesPage struct {
	Modules []moduleRow
	// Chosen is false until the page has been saved once.
	Chosen bool
	// Confirm names the modules that would also go off, when the adult is
	// asked first.
	Confirm string
}

type moduleRow struct {
	ID, Name, Description string
	On                    bool
	Needs                 string // names of the modules it requires
}

func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	st, err := s.Modules.State(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderModules(w, r, http.StatusOK, st.On, st.Chosen, "", "")
}

func (s *Server) renderModules(w http.ResponseWriter, r *http.Request, status int, on map[string]bool, chosen bool, confirm, flash string) {
	reg := s.Modules.Registry()
	p := modulesPage{Chosen: chosen, Confirm: confirm}
	for _, m := range reg.All() {
		p.Modules = append(p.Modules, moduleRow{
			ID: m.ID(), Name: m.Name(), Description: m.Description(),
			On: on[m.ID()], Needs: s.moduleNames(m.Requires()),
		})
	}
	v := s.view(r, "Modules", p)
	v.Nav = "modules"
	v.Flash = flash
	s.render(w, r, status, "admin_modules", v)
}

func (s *Server) handleModulesUpdate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	st, err := s.Modules.State(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	reg := s.Modules.Registry()
	checked := r.PostForm["module"]
	want := map[string]bool{}
	for _, m := range reg.All() {
		want[m.ID()] = slices.Contains(checked, m.ID())
	}
	c := reg.Plan(st.On, want)
	if len(c.AlsoOff) > 0 && r.PostForm.Get("confirm") == "" {
		// Ask first: show the page as they left it, with a warning.
		s.renderModules(w, r, http.StatusOK, want, st.Chosen, s.moduleNames(c.AlsoOff), "")
		return
	}
	if err := s.Modules.Set(ctx, hhOf(r), &actor(r).Person.ID, c.Final); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash := "Saved."
	if len(c.AlsoOn) > 0 {
		flash = "Saved. " + s.moduleNames(c.AlsoOn) + " is on too, because another module needs it."
	}
	s.renderModules(w, r, http.StatusOK, c.Final, true, "", flash)
}

// moduleNames is the modules' names, e.g. "Rewards and Photos".
func (s *Server) moduleNames(ids []string) string {
	var names []string
	for _, id := range ids {
		if m, ok := s.Modules.Registry().Get(id); ok {
			names = append(names, m.Name())
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
