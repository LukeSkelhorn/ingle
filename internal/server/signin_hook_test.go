package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type outsideSignIn struct {
	*twoHouseholds
	mumID, dadID int64
	signOuts     atomic.Int32
}

const workosLogout = "https://auth.example/logout"

func newOutsideSignIn(t *testing.T) *outsideSignIn {
	t.Helper()
	x := &outsideSignIn{twoHouseholds: newTwoHouseholds(t)}
	x.mumID = x.personIn(x.a, "Mum").ID
	x.dadID = x.personIn(x.b, "Dad").ID

	srv := x.h.srv
	srv.SignIn = &SignIn{
		URL: "/auth/signin",
		OnSignOut: func(*http.Request) string {
			x.signOuts.Add(1)
			return workosLogout
		},
	}
	srv.Household.SetExternalSignIn(true)
	srv.Routes = func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
			hh, _ := strconv.ParseInt(r.URL.Query().Get("hh"), 10, 64)
			pid, _ := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64)
			if err := s.StartSession(w, r, hh, pid); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
		})
	}
	x.h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(x.h.ts.Close)
	return x
}

func (x *outsideSignIn) signIn(host string, hh, personID int64) *device {
	x.h.t.Helper()
	d := x.on(host)
	expectRedirect(x.h.t, d.get("/auth/callback?hh="+itoa(hh)+"&person="+itoa(personID)), "/admin")
	return d
}

func TestOutsideSignInStartsASession(t *testing.T) {
	x := newOutsideSignIn(t)

	phone := x.signIn("a.test", x.a, x.mumID)
	expect(t, phone.get("/admin"), http.StatusOK, "The Smiths")
	expect(t, phone.get("/admin/people"), http.StatusOK, "Alfie")
	expectNot(t, phone.get("/admin/people"), "Bea", "· Sign-in")
	expect(t, phone.get("/admin/people/"+itoa(x.beaID)), http.StatusNotFound)

	expect(t, x.signIn("", x.b, x.dadID).get("/admin"), http.StatusOK, "The Joneses")
}

func TestOutsideSignInRefusesOthers(t *testing.T) {
	x := newOutsideSignIn(t)
	tests := []struct {
		name   string
		host   string
		hh, id int64
	}{
		{"person of another household", "a.test", x.a, x.dadID},
		{"address of another household", "a.test", x.b, x.dadID},
		{"a child", "a.test", x.a, x.alfieID},
		{"nobody", "a.test", x.a, 0},
		{"no household", "", 0, x.mumID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := x.on(tt.host)
			expect(t, d.get("/auth/callback?hh="+itoa(tt.hh)+"&person="+itoa(tt.id)), http.StatusForbidden)
			expectRedirect(t, d.get("/admin"), "/auth/signin")
		})
	}

	ctx := store.WithHousehold(t.Context(), x.b)
	if err := x.h.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: &[]int64{x.h.clock().Unix()}[0], HouseholdID: x.b, ID: x.dadID}); err != nil {
		t.Fatal(err)
	}
	expect(t, x.on("b.test").get("/auth/callback?hh="+itoa(x.b)+"&person="+itoa(x.dadID)), http.StatusForbidden)
}

func TestOutsideSignInReplacesIngles(t *testing.T) {
	x := newOutsideSignIn(t)
	d := x.on("a.test")

	expectRedirect(t, d.get("/signin"), "/auth/signin")
	expectRedirect(t, d.get("/admin"), "/auth/signin")
	expect(t, d.post("/signin", url.Values{"email": {"mum@example.com"}, "password": {"correct horse battery"}}), http.StatusNotFound)
	expect(t, d.get("/setup"), http.StatusNotFound)
	expect(t, d.post("/setup", setupValues), http.StatusNotFound)

	phone := x.signIn("a.test", x.a, x.mumID)
	page := phone.get("/admin/people/" + itoa(x.mumID))
	expect(t, page, http.StatusOK, "Remove Mum&#39;s PIN?")
	expectNot(t, page, "Save sign-in", `name="password"`)
	expect(t, phone.post("/admin/people/"+itoa(x.mumID)+"/login", url.Values{"email": {"m@example.com"}, "password": {"another long one"}, "password_confirm": {"another long one"}}), http.StatusNotFound)
	expect(t, phone.post("/admin/people/"+itoa(x.mumID)+"/login/clear", url.Values{}), http.StatusNotFound)

	c, err := x.h.st.Q.CreateHousehold(store.AsSystem(t.Context()), dbq.CreateHouseholdParams{Name: "Home", CreatedAt: x.h.clock().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	x.h.srv.ResolveHousehold = func(r *http.Request) (int64, bool, error) {
		switch r.Host {
		case "a.test":
			return x.a, true, nil
		case "c.test":
			return c.ID, true, nil
		}
		return 0, false, nil
	}
	expectRedirect(t, x.on("c.test").get("/"), "/auth/signin")
}

func TestOutsideSignInSignOut(t *testing.T) {
	x := newOutsideSignIn(t)
	phone := x.signIn("a.test", x.a, x.mumID)

	expectRedirect(t, phone.post("/signout", nil), workosLogout)
	if n := x.signOuts.Load(); n != 1 {
		t.Errorf("OnSignOut ran %d times, want 1", n)
	}
	expectRedirect(t, phone.get("/admin"), "/auth/signin")

	expectRedirect(t, x.aW.post("/admin/lock", nil), "/")
	if n := x.signOuts.Load(); n != 1 {
		t.Errorf("locking a display ran OnSignOut: %d times", n)
	}
}

func TestOutsideSignInLastAdult(t *testing.T) {
	x := newOutsideSignIn(t)
	phone := x.signIn("a.test", x.a, x.mumID)
	mum := "/admin/people/" + itoa(x.mumID)

	expect(t, phone.post(mum+"/pin/clear", url.Values{}), http.StatusOK, "PIN removed")
	if p := x.personIn(x.a, "Mum"); p.PinHash != nil {
		t.Error("PIN still set")
	}
	expect(t, phone.post(mum+"/archive", url.Values{}), http.StatusConflict, "At least one adult")
}

func TestOutsideSignInSessionIdle(t *testing.T) {
	x := newOutsideSignIn(t)
	x.h.auth.SetPersonalIdle(time.Hour)
	defer x.h.auth.SetPersonalIdle(0)
	phone := x.signIn("a.test", x.a, x.mumID)

	x.h.advance(50 * time.Minute)
	expect(t, phone.get("/admin"), http.StatusOK)
	x.h.advance(50 * time.Minute)
	expect(t, phone.get("/admin"), http.StatusOK)
	x.h.advance(61 * time.Minute)
	expectRedirect(t, phone.get("/admin"), "/auth/signin")
}
