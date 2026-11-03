// Package server wires HTTP routes for the Domus board.
package server

import (
	"bytes"
	"context"
	"database/sql"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/web"
)

// Server serves the Domus web UI and API.
type Server struct {
	db     *sql.DB
	locale locale.Locale
	log    *slog.Logger
	tmpl   *template.Template
	now    func() time.Time
}

// New builds a Server. db may be nil in tests that do not touch storage.
func New(db *sql.DB, loc locale.Locale, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(web.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{db: db, locale: loc, log: log, tmpl: tmpl, now: time.Now}, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err) // "static" is embedded at compile time
	}
	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /clock", s.handleClock)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", http.StripPrefix("/static/", files))
	// The manifest and service worker must live at the root so the SW can
	// control the whole origin.
	mux.HandleFunc("GET /manifest.webmanifest", serveFile(static, "manifest.webmanifest", "application/manifest+json"))
	mux.HandleFunc("GET /sw.js", serveFile(static, "sw.js", "text/javascript; charset=utf-8"))
	return mux
}

type clockView struct {
	ISO, Time, Weekday, Date string
}

func (s *Server) clock() clockView {
	now := s.locale.In(s.now())
	return clockView{
		ISO:     now.Format(time.RFC3339),
		Time:    s.locale.FormatTime(now),
		Weekday: now.Weekday().String(),
		Date:    s.locale.FormatDate(now),
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "layout", s.clock())
}

func (s *Server) handleClock(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "clock", s.clock())
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			s.log.ErrorContext(r.Context(), "health check failed", slog.Any("err", err))
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.ErrorContext(r.Context(), "render template", slog.String("template", name), slog.Any("err", err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func serveFile(fsys fs.FS, name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}
