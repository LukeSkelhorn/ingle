package server

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/avatar"
	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
	"github.com/lukeskelhorn/domus/internal/valid"
	"github.com/lukeskelhorn/domus/web"
)

// pages holds one template set per page: layout + partials + that page, so
// every page can define its own "title" and "content" blocks.
type pages struct {
	base *template.Template
	sets map[string]*template.Template
}

func loadPages(funcs template.FuncMap) (*pages, error) {
	base, err := template.New("").Funcs(funcs).ParseFS(web.Templates, "templates/layout.html", "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}
	files, err := fs.Glob(web.Templates, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	p := &pages{base: base, sets: map[string]*template.Template{}}
	for _, f := range files {
		set, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := set.ParseFS(web.Templates, f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		p.sets[strings.TrimSuffix(path.Base(f), ".html")] = set
	}
	return p, nil
}

func (p *pages) execute(w io.Writer, page string, data any) error {
	set, ok := p.sets[page]
	if !ok {
		return fmt.Errorf("no page %q", page)
	}
	return set.ExecuteTemplate(w, "layout", data)
}

func (p *pages) partial(w io.Writer, name string, data any) error {
	return p.base.ExecuteTemplate(w, name, data)
}

// view is the data every page gets. Data carries the page-specific part.
type view struct {
	Title     string
	Nav       string // active admin section
	Household string
	Viewer    viewer
	Flash     string
	Errors    valid.Errors
	Data      any
}

// viewer describes who is looking at the page.
type viewer struct {
	IsDisplay   bool
	DisplayName string
	Adult       *dbq.Person
	Kind        string // auth.KindDisplay or auth.KindPersonal when Adult is set
	// IdleSeconds is the client-side auto-lock for admin on a display.
	IdleSeconds int
}

// OnDisplayAdmin reports whether an adult is unlocked on a household display
// (so the page should auto-lock when idle).
func (v viewer) OnDisplayAdmin() bool { return v.Adult != nil && v.Kind == auth.KindDisplay }

// avatarView is what the "avatar" partial needs.
type avatarView struct {
	Name    string
	Colour  string
	Emoji   string
	Photo   string
	Initial string
	Size    string // "sm", "md", "lg"
}

func avatarOf(p dbq.Person, size string) avatarView {
	v := avatarView{Name: p.Name, Colour: p.Colour, Size: size}
	switch kind, val := avatar.Parse(p.Avatar); kind {
	case avatar.Emoji:
		v.Emoji = val
	case avatar.Photo:
		v.Photo = "/avatars/" + val
	default:
		if r := []rune(p.Name); len(r) > 0 {
			v.Initial = strings.ToUpper(string(r[0]))
		}
	}
	return v
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"money":      func(pence int64) string { return s.Locale.FormatMoney(pence) },
		"moneyInput": locale.FormatMoneyInput,
		"avatar":     avatarOf,
		"deref": func(p any) any {
			switch v := p.(type) {
			case *string:
				if v == nil {
					return ""
				}
				return *v
			case *int64:
				if v == nil {
					return int64(0)
				}
				return *v
			}
			return p
		},
		"when": func(unix int64) string {
			loc := s.Locale
			if s.Settings != nil {
				loc.Location = s.Settings.Location(context.Background(), s.HouseholdID)
			}
			t := time.Unix(unix, 0)
			return loc.FormatDate(t) + " " + loc.FormatTime(t)
		},
		"weekdayShort": func(d time.Weekday) string { return d.String()[:3] },
		"dict":         dict,
		"list":         func(v ...string) []string { return v },
		"activity":     describeActivity,
	}
}

// dict builds a map from alternating keys and values, for passing several
// values to a partial.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}
