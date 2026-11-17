package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/app"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestRunServesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INGLE_ADDR", freeAddr(t))
	t.Setenv("INGLE_DB_PATH", filepath.Join(dir, "ingle.db"))
	t.Setenv("INGLE_BACKUP_DIR", filepath.Join(dir, "backups"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, app.Options{Version: "test"}) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		err := app.Healthcheck(context.Background())
		if err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthz never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	path, err := app.Backup(context.Background())
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("backup file: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunManyHouseholds(t *testing.T) {
	dir := t.TempDir()
	addr := freeAddr(t)
	t.Setenv("INGLE_ADDR", addr)
	t.Setenv("INGLE_DB_PATH", filepath.Join(dir, "ingle.db"))
	t.Setenv("INGLE_BACKUP_KEEP", "0")

	opts := app.Options{
		Version:          "test",
		ResolveHousehold: func(*http.Request) (int64, bool, error) { return 0, false, nil },
		Routes: func(mux *http.ServeMux, h app.Helpers) {
			mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "household %d", app.HouseholdOf(r))
			})
			mux.Handle("GET /admin/billing", h.RequireAdmin(http.NotFoundHandler()))
		},
		AdminLinks: []app.AdminLink{{Label: "Billing", Path: "/admin/billing"}},
		Jobs:       app.JobsElsewhere,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, opts) }()
	waitHealthy(t, done)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Location"), string(b)
	}
	if code, _, body := get("/hello"); code != http.StatusOK || body != "household 0" {
		t.Errorf("/hello = %d %q", code, body)
	}
	if code, loc, _ := get("/admin/billing"); code != http.StatusSeeOther || loc != "/signin" {
		t.Errorf("/admin/billing = %d → %q, want sign in", code, loc)
	}
	if code, loc, _ := get("/"); code != http.StatusSeeOther || loc != "/signin" {
		t.Errorf("/ = %d → %q, want sign in", code, loc)
	}
	if code, _, _ := get("/setup"); code != http.StatusNotFound {
		t.Errorf("/setup = %d, want 404", code)
	}

	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan error, 1)
	go func() { jobsDone <- app.RunJobs(jobsCtx, opts) }()
	time.Sleep(200 * time.Millisecond)
	stopJobs()
	select {
	case err := <-jobsDone:
		if err != nil {
			t.Fatalf("RunJobs: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("RunJobs did not return after cancel")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func waitHealthy(t *testing.T, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := app.Healthcheck(context.Background())
		if err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthz never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunRefusesSignInWithoutURL(t *testing.T) {
	t.Setenv("INGLE_ADDR", freeAddr(t))
	t.Setenv("INGLE_DB_PATH", filepath.Join(t.TempDir(), "ingle.db"))
	t.Setenv("INGLE_BACKUP_KEEP", "0")
	err := app.Run(t.Context(), app.Options{
		ResolveHousehold: func(*http.Request) (int64, bool, error) { return 0, false, nil },
		SignIn:           &app.SignIn{},
	})
	if err == nil || !strings.Contains(err.Error(), "SignIn.URL") {
		t.Fatalf("Run = %v, want an error about SignIn.URL", err)
	}
}

func TestCreateHousehold(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("INGLE_ADDR", addr)
	t.Setenv("INGLE_DB_PATH", filepath.Join(t.TempDir(), "ingle.db"))
	t.Setenv("INGLE_BACKUP_KEEP", "0")

	opts := app.Options{
		ResolveHousehold: func(*http.Request) (int64, bool, error) { return 0, false, nil },
		SignIn:           &app.SignIn{URL: "/outside"},
		Jobs:             app.JobsElsewhere,
		Routes: func(mux *http.ServeMux, h app.Helpers) {
			mux.HandleFunc("POST /make", func(w http.ResponseWriter, r *http.Request) {
				hh, person, err := h.CreateHousehold(r.Context(), app.CreateHouseholdInput{
					Name: r.FormValue("name"), Timezone: "Europe/London", Adult: "Mum",
					Colour: r.FormValue("colour"), Emoji: r.FormValue("emoji"), Email: r.FormValue("email"),
				})
				var fe app.FieldErrors
				if errors.As(err, &fe) {
					http.Error(w, "fields: "+strings.Join(slices.Sorted(maps.Keys(fe)), ","), http.StatusUnprocessableEntity)
					return
				}
				if err == nil {
					err = h.StartSession(w, r, hh, person)
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, "/admin", http.StatusSeeOther)
			})
			mux.Handle("POST /pin", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := app.AdultOf(r); !ok {
					http.Error(w, "no adult", http.StatusInternalServerError)
					return
				}
				var fe app.FieldErrors
				if err := h.SetPIN(r, r.FormValue("pin")); errors.As(err, &fe) {
					http.Error(w, "fields: "+strings.Join(slices.Sorted(maps.Keys(fe)), ","), http.StatusUnprocessableEntity)
					return
				} else if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})))
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, opts) }()
	defer func() {
		cancel()
		<-done
	}()
	waitHealthy(t, done)

	type browser struct{ *http.Client }
	newBrowser := func() browser {
		jar, _ := cookiejar.New(nil)
		return browser{&http.Client{Jar: jar}}
	}
	do := func(b browser, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, "http://"+addr+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	mum := newBrowser()
	if code, body := do(mum, http.MethodPost, "/make", url.Values{"name": {"The Smiths"}, "colour": {"#0090FF"}, "emoji": {app.PersonEmojis()[0]}, "email": {"mum@example.com"}}); code != http.StatusOK || !strings.Contains(body, "The Smiths") {
		t.Fatalf("make = %d\n%s", code, body)
	}
	if code, body := do(mum, http.MethodGet, "/admin/people", nil); code != http.StatusOK || !strings.Contains(body, "Mum") {
		t.Errorf("people = %d", code)
	}
	if code, body := do(mum, http.MethodPost, "/pin", url.Values{"pin": {"12a"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "fields: pin") {
		t.Errorf("bad pin = %d %s", code, body)
	}
	if code, _ := do(mum, http.MethodPost, "/pin", url.Values{"pin": {"2468"}}); code != http.StatusNoContent {
		t.Errorf("pin = %d", code)
	}
	if code, _ := do(mum, http.MethodGet, "/admin/people", nil); code != http.StatusOK {
		t.Errorf("after setting the PIN, people = %d", code)
	}

	dad := newBrowser()
	if code, body := do(dad, http.MethodPost, "/make", url.Values{"name": {"The Joneses"}, "colour": {app.PersonColours()[0]}, "email": {"dad@example.com"}}); code != http.StatusOK || !strings.Contains(body, "The Joneses") {
		t.Fatalf("second make = %d\n%s", code, body)
	}
	if _, body := do(dad, http.MethodGet, "/admin/people", nil); strings.Contains(body, "The Smiths") {
		t.Error("the Joneses see the Smiths")
	}

	code, body := do(newBrowser(), http.MethodPost, "/make", url.Values{"name": {"Again"}, "colour": {"blue"}, "emoji": {"🦖x"}, "email": {"mum@example.com"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "fields: colour") || !strings.Contains(body, "emoji") {
		t.Errorf("bad input = %d %s", code, body)
	}
	if code, body := do(newBrowser(), http.MethodPost, "/make", url.Values{"name": {"Again"}, "colour": {"#0090FF"}, "email": {"MUM@example.com"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "fields: email") {
		t.Errorf("taken email = %d %s", code, body)
	}
}
