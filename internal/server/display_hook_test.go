package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/module"
)

func newDisplayHook(t *testing.T) (*twoHouseholds, int64) {
	t.Helper()
	x := newTwoHouseholds(t)
	srv := x.h.srv
	srv.DisplayActions = []module.NavItem{{Label: "Add a display", Path: "/pair/add"}}
	srv.Routes = func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /pair/done", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			hh, _ := strconv.ParseInt(q.Get("hh"), 10, 64)
			by, _ := strconv.ParseInt(q.Get("by"), 10, 64)
			if err := s.RegisterDisplay(w, r, hh, q.Get("name"), by); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
		})
	}
	x.h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(x.h.ts.Close)
	return x, x.personIn(x.a, "Mum").ID
}

func pairPath(hh, by int64, name string) string {
	return "/pair/done?" + url.Values{"hh": {itoa(hh)}, "by": {itoa(by)}, "name": {name}}.Encode()
}

func TestRegisterDisplayHookMakesADisplay(t *testing.T) {
	x, mum := newDisplayHook(t)

	tablet := x.on("")
	expectRedirect(t, tablet.get(pairPath(x.a, mum, "Hall")), "/")
	expect(t, tablet.get("/"), http.StatusOK, "The Smiths")
	expectRedirect(t, tablet.get("/admin"), "/admin/unlock")

	devices := x.aW.get("/admin/devices")
	expect(t, devices, http.StatusOK, "Hall", `href="/pair/add"`, "Add a display")
	expectNot(t, x.bW.get("/admin/devices"), "Hall")
}

func TestRegisterDisplayHookRefusesOthers(t *testing.T) {
	x, mum := newDisplayHook(t)
	tests := []struct {
		name   string
		host   string
		hh, by int64
		label  string
	}{
		{"adult of another household", "", x.b, mum, "Hall"},
		{"address of another household", "b.test", x.a, mum, "Hall"},
		{"a child", "", x.a, x.alfieID, "Hall"},
		{"nobody", "", x.a, 0, "Hall"},
		{"no name", "", x.a, mum, " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := x.on(tt.host)
			expect(t, d.get(pairPath(tt.hh, tt.by, tt.label)), http.StatusForbidden)
			expectRedirect(t, d.get("/"), "/signin")
		})
	}
}
