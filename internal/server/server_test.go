package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/locale"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := New(nil, locale.UK(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 30 June 2026 23:30 UTC is 1 July 00:30 BST.
	s.now = func() time.Time { return time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC) }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (status int, header http.Header, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

func TestRoutes(t *testing.T) {
	ts := newTestServer(t)
	tests := []struct {
		path        string
		status      int
		contentType string
		contains    string
	}{
		{"/healthz", http.StatusOK, "text/plain", "ok"},
		{"/", http.StatusOK, "text/html", "01/07/2026"},
		{"/", http.StatusOK, "text/html", `rel="manifest"`},
		{"/clock", http.StatusOK, "text/html", "00:30"},
		{"/manifest.webmanifest", http.StatusOK, "application/manifest+json", `"display": "standalone"`},
		{"/sw.js", http.StatusOK, "text/javascript", "domus-static"},
		{"/static/htmx.min.js", http.StatusOK, "javascript", "htmx"},
		{"/static/icons/icon-192.png", http.StatusOK, "image/png", ""},
		{"/nope", http.StatusNotFound, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			status, header, body := get(t, ts, tt.path)
			if status != tt.status {
				t.Fatalf("status = %d, want %d", status, tt.status)
			}
			if ct := header.Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want to contain %q", ct, tt.contentType)
			}
			if !strings.Contains(body, tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
		})
	}
}
