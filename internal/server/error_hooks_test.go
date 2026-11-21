package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lukeskelhorn/ingle/internal/store"
)

type logRecords struct {
	mu   sync.Mutex
	recs []slog.Record
	hh   []int64
}

func (l *logRecords) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecords) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logRecords) WithGroup(string) slog.Handler            { return l }
func (l *logRecords) Handle(ctx context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r)
	l.hh = append(l.hh, store.HouseholdFrom(ctx))
	return nil
}

func TestPanicIsLoggedWithTheHousehold(t *testing.T) {
	x := newTwoHouseholds(t)
	logs := &logRecords{}
	srv := x.h.srv
	srv.Log = slog.New(logs)
	srv.Routes = func(s *Server, mux *http.ServeMux) {
		mux.Handle("GET /boom", s.RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })))
	}
	x.h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(x.h.ts.Close)

	expect(t, x.aW.get("/boom"), http.StatusInternalServerError, "Something went wrong")

	logs.mu.Lock()
	defer logs.mu.Unlock()
	for i, r := range logs.recs {
		if r.Level == slog.LevelError && r.Message == "request panicked" {
			if logs.hh[i] != x.a {
				t.Errorf("household = %d, want %d", logs.hh[i], x.a)
			}
			return
		}
	}
	t.Fatalf("no panic logged: %v", logs.recs)
}

func TestHeadScriptsAreLoaded(t *testing.T) {
	x := newTwoHouseholds(t)
	srv := x.h.srv
	srv.HeadScripts = []string{"/extra/errors.js"}
	x.h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(x.h.ts.Close)

	expect(t, x.aW.get("/admin"), http.StatusOK, `<script src="/extra/errors.js" defer></script>`)
	expect(t, x.on("").get("/signin"), http.StatusOK, `<script src="/extra/errors.js" defer></script>`)
}
