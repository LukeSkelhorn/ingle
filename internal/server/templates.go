package server

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
	"github.com/lukeskelhorn/ingle/web"
)

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

// clone fails once p has executed, so the master set must never be executed.
func (p *pages) clone() (*pages, error) {
	base, err := p.base.Clone()
	if err != nil {
		return nil, err
	}
	c := &pages{base: base, sets: make(map[string]*template.Template, len(p.sets))}
	for name, set := range p.sets {
		if c.sets[name], err = set.Clone(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (p *pages) bind(funcs template.FuncMap) {
	p.base.Funcs(funcs)
	for _, set := range p.sets {
		set.Funcs(funcs)
	}
}

type pagePool struct {
	master *pages
	pool   sync.Pool
}

func newPagePool(master *pages) *pagePool {
	return &pagePool{master: master}
}

func (pp *pagePool) get() (*pages, error) {
	if p, ok := pp.pool.Get().(*pages); ok {
		return p, nil
	}
	return pp.master.clone()
}

func (pp *pagePool) put(p *pages) { pp.pool.Put(p) }

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

type view struct {
	Title          string
	Nav            string
	Household      string
	Viewer         viewer
	Flash          string
	Errors         valid.Errors
	Data           any
	Version        string
	ExternalSignIn bool
	Edit           string
}

type viewer struct {
	IsDisplay          bool
	DisplayName        string
	Adult              *dbq.Person
	Kind               string
	IdleSeconds        int
	ScreensaverSeconds int
	CalendarSeconds    int
}

func (v viewer) OnDisplayAdmin() bool { return v.Adult != nil && v.Kind == auth.KindDisplay }

type avatarView struct {
	Name    string
	Colour  string
	Emoji   string
	Photo   string
	Initial string
	Size    string
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

func (s *Server) householdFuncs(ctx context.Context) template.FuncMap {
	return template.FuncMap{
		"moduleOn":       func(id string) bool { return s.moduleOn(ctx, id) },
		"adminNav":       func() adminNav { return s.adminNav(ctx) },
		"householdState": func() HouseholdState { return s.state(ctx, householdIn(ctx)) },
		"when": func(unix int64) string {
			loc := s.Locale
			if s.Settings != nil {
				loc.Location = s.Settings.Location(ctx, householdIn(ctx))
			}
			t := time.Unix(unix, 0)
			return loc.FormatDate(t) + " " + loc.FormatTime(t)
		},
		"dayOf": func(unix int64) string {
			loc := time.UTC
			if s.Settings != nil {
				loc = s.Settings.Location(ctx, householdIn(ctx))
			}
			return time.Unix(unix, 0).In(loc).Format("Mon 2 Jan")
		},
	}
}

func (s *Server) funcs() template.FuncMap {
	fm := s.householdFuncs(context.Background())
	for k, v := range s.staticFuncs() {
		fm[k] = v
	}
	return fm
}

func (s *Server) staticFuncs() template.FuncMap {
	return template.FuncMap{
		"money":      func(pence int64) string { return s.Locale.FormatMoney(pence) },
		"moneyInput": locale.FormatMoneyInput,
		"updatedAgo": func(t time.Time) string { return updatedAgo(s.now(), t) },
		"avatar":     avatarOf,
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
		"weekdayShort": func(d time.Weekday) string { return d.String()[:3] },
		"names":        func(ps []dbq.Person) string { return joinNames(ps, "and") },
		"orNames":      func(ps []dbq.Person) string { return joinNames(ps, "or") },
		"shortDate":    shortDate,
		"worth":        s.worth,
		"togo":         s.togo,
		"pts":          pts,
		"inc":          func(i int) int { return i + 1 },
		"signedPts":    signedPts,
		"net":          s.net,
		"change":       s.change,
		"neg":          func(n int64) int64 { return -n },
		"percent": func(done, total int) int {
			if total == 0 {
				return 0
			}
			return done * 100 / total
		},
		"dict":          dict,
		"chosen":        chosen,
		"list":          func(v ...string) []string { return v },
		"activity":      describeActivity,
		"activityLine":  s.activityLine,
		"asset":         func(name string) string { return "/static/" + name + "?v=" + web.StaticVersion() },
		"activityActor": activityActor,
		"headScripts":   func() []string { return s.HeadScripts },
	}
}

func shortDate(ymd string) string {
	t, err := time.Parse("2006-01-02", ymd)
	if err != nil {
		return ymd
	}
	return t.Format("Mon 2 Jan")
}

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

func (s *Server) togo(points, pence int64) string {
	var parts []string
	if points > 0 {
		parts = append(parts, pts(points))
	}
	if pence > 0 {
		parts = append(parts, s.Locale.FormatMoney(pence))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " and ") + " to go"
}

func pts(n int64) string {
	unit := " pts"
	if n == 1 || n == -1 {
		unit = " pt"
	}
	return minus(n) + unit
}

func signedPts(n int64) string {
	if n > 0 {
		return "+" + strconv.FormatInt(n, 10)
	}
	return minus(n)
}

func minus(n int64) string {
	if n < 0 {
		return "−" + strconv.FormatInt(-n, 10)
	}
	return strconv.FormatInt(n, 10)
}

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

func (s *Server) change(points, pence int64) string {
	var parts []string
	if points != 0 {
		parts = append(parts, signedPts(points)+strings.TrimPrefix(pts(points), minus(points)))
	}
	switch {
	case pence > 0:
		parts = append(parts, "+"+s.Locale.FormatMoney(pence))
	case pence < 0:
		parts = append(parts, "−"+s.Locale.FormatMoney(-pence))
	}
	return strings.Join(parts, " ")
}

func chosen(sel any, id int64) bool {
	switch v := sel.(type) {
	case int64:
		return v == id
	case []int64:
		return slices.Contains(v, id)
	}
	return false
}

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

func joinNames(ps []dbq.Person, last string) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return joinWords(names, last)
}

func joinWords(words []string, last string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + last + " " + words[len(words)-1]
}
