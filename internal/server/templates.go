package server

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
	"github.com/lukeskelhorn/ingle/web"
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
	Version   string // the Ingle build, shown in admin
}

// viewer describes who is looking at the page.
type viewer struct {
	IsDisplay   bool
	DisplayName string
	Adult       *dbq.Person
	Kind        string // auth.KindDisplay or auth.KindPersonal when Adult is set
	// IdleSeconds is the client-side auto-lock for admin on a display.
	IdleSeconds int
	// ScreensaverSeconds is how long a display waits, untouched, before
	// showing the screensaver; 0 means never (always 0 off a display).
	ScreensaverSeconds int
	// CalendarSeconds is how long a display shows the board calendar,
	// untouched, before going back to the chores; 0 means never (always 0
	// off a display).
	CalendarSeconds int
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
		// plainAvatar is an avatar without its photo (initial or emoji),
		// for the locked display, where /avatars/ isn't served.
		"plainAvatar": func(p dbq.Person, size string) avatarView {
			v := avatarOf(p, size)
			if v.Photo != "" {
				v.Photo = ""
				if r := []rune(p.Name); len(r) > 0 {
					v.Initial = strings.ToUpper(string(r[0]))
				}
			}
			return v
		},
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
		// dayOf is a moment's local day, e.g. "Thu 5 Nov".
		"dayOf": func(unix int64) string {
			loc := time.UTC
			if s.Settings != nil {
				loc = s.Settings.Location(context.Background(), s.HouseholdID)
			}
			return time.Unix(unix, 0).In(loc).Format("Mon 2 Jan")
		},
		"weekdayShort": func(d time.Weekday) string { return d.String()[:3] },
		// names and orNames list people: "Isla and Mum", "Luke or Sam".
		"names":     func(ps []dbq.Person) string { return joinNames(ps, "and") },
		"orNames":   func(ps []dbq.Person) string { return joinNames(ps, "or") },
		"shortDate": shortDate,
		"worth":     s.worth,
		"pts":       pts,
		"signedPts": signedPts,
		"net":       s.net,
		"percent": func(done, total int) int {
			if total == 0 {
				return 0
			}
			return done * 100 / total
		},
		"dict":     dict,
		"list":     func(v ...string) []string { return v },
		"activity": describeActivity,
		// asset is a static file's URL with the build's asset hash, so a new
		// build's CSS and JS are never served from an old cache.
		"asset":         func(name string) string { return "/static/" + name + "?v=" + web.StaticVersion() },
		"activityActor": activityActor,
	}
}

// shortDate formats a stored YYYY-MM-DD as e.g. "Mon 2 Nov" (UK order).
func shortDate(ymd string) string {
	t, err := time.Parse("2006-01-02", ymd)
	if err != nil {
		return ymd
	}
	return t.Format("Mon 2 Jan")
}

// worth describes what a chore is worth, e.g. "5 pts · £0.50", or "" if
// nothing.
func (s *Server) worth(points, pence int64) string {
	var parts []string
	if points > 0 {
		parts = append(parts, pts(points))
	}
	if pence > 0 {
		parts = append(parts, s.Locale.FormatMoney(pence))
	}
	return strings.Join(parts, " · ")
}

// pts formats a points count: "1 pt", "12 pts", "−5 pts" (with a real
// minus sign).
func pts(n int64) string {
	unit := " pts"
	if n == 1 || n == -1 {
		unit = " pt"
	}
	return minus(n) + unit
}

// signedPts formats a change in points: "+5", "−5".
func signedPts(n int64) string {
	if n > 0 {
		return "+" + strconv.FormatInt(n, 10)
	}
	return minus(n)
}

// minus formats n with a real minus sign when it's negative.
func minus(n int64) string {
	if n < 0 {
		return "−" + strconv.FormatInt(-n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// net describes a net amount that can be negative, e.g. "−5 pts · £0.50",
// or "" if nothing. Unlike worth, negative points are shown.
func (s *Server) net(points, pence int64) string {
	var parts []string
	if points != 0 {
		parts = append(parts, pts(points))
	}
	if pence > 0 {
		parts = append(parts, s.Locale.FormatMoney(pence))
	}
	return strings.Join(parts, " · ")
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

// joinNames lists people's names as "A, B and C" (or "A, B or C").
func joinNames(ps []dbq.Person, last string) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + last + " " + names[len(names)-1]
}
