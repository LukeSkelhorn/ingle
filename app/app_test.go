package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/app"
)

// freeAddr returns a loopback address with a port nothing is listening on.
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
		Version: "test",
		// No address names a household here, so only a cookie could.
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
	// No household was made at startup, so there's none to set up.
	if code, loc, _ := get("/"); code != http.StatusSeeOther || loc != "/signin" {
		t.Errorf("/ = %d → %q, want sign in", code, loc)
	}
	if code, _, _ := get("/setup"); code != http.StatusNotFound {
		t.Errorf("/setup = %d, want 404", code)
	}

	// A worker runs the jobs beside it and stops with its context.
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

// waitHealthy waits for the server Run started to answer /healthz.
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
