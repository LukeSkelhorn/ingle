package server

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/emoji"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
	"github.com/lukeskelhorn/domus/internal/valid"
)

type choreRow struct {
	Chore    dbq.Chore
	Assignee *dbq.Person
	Schedule string
}

// Admin chores list sorts.
const (
	sortPoints = "points"
	sortMoney  = "money"
	// personPool is the person filter for chores anyone can claim.
	personPool = "pool"
)

// choresQuery is the chores list's filter and sort, kept in the URL so
// going into a chore and back returns to the same list.
type choresQuery struct {
	Person string // "", personPool or a person's ID
	Sort   string // "", sortPoints or sortMoney
}

// parseChoresQuery reads the filter and sort from q, ignoring a person who
// isn't active in the household and any sort it doesn't know.
func parseChoresQuery(q url.Values, people []dbq.Person) choresQuery {
	var out choresQuery
	switch p := q.Get("person"); p {
	case personPool:
		out.Person = p
	default:
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			for _, pp := range people {
				if pp.ID == id {
					out.Person = p
				}
			}
		}
	}
	if v := q.Get("sort"); v == sortPoints || v == sortMoney {
		out.Sort = v
	}
	return out
}

// Encode is the query string, without the empty parts.
func (q choresQuery) Encode() string {
	v := url.Values{}
	if q.Person != "" {
		v.Set("person", q.Person)
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	return v.Encode()
}

// Path is the list's URL for q.
func (q choresQuery) Path() string {
	if e := q.Encode(); e != "" {
		return "/admin/chores?" + e
	}
	return "/admin/chores"
}

// choreBack is where to return after adding or archiving a chore: the
// form's back value if it's the chores list with a valid filter, else the
// plain list. Anything else (another page, another site) is ignored.
func choreBack(back string, people []dbq.Person) string {
	u, err := url.Parse(back)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path != "/admin/chores" {
		return "/admin/chores"
	}
	return parseChoresQuery(u.Query(), people).Path()
}

// choresPage is the admin chores list.
type choresPage struct {
	Rows   []choreRow
	People []dbq.Person
	Q      choresQuery
	Person *dbq.Person // the person filtered on
	Pool   bool        // filtered to up for grabs
	Total  int         // chores before filtering
}

// Heading is "Chores", "Chores · 6 for Alfie" or "Chores · 3 up for grabs".
func (p choresPage) Heading() string {
	switch {
	case p.Person != nil:
		return fmt.Sprintf("Chores · %d for %s", len(p.Rows), p.Person.Name)
	case p.Pool:
		return fmt.Sprintf("Chores · %d up for grabs", len(p.Rows))
	}
	return "Chores"
}

// ChipURL is the list filtered to person ("" for everyone), keeping the sort.
func (p choresPage) ChipURL(person string) string {
	return choresQuery{Person: person, Sort: p.Q.Sort}.Path()
}

// Back is this list, for the chore pages to come back to.
func (p choresPage) Back() string { return p.Q.Path() }

// NewURL is "Add a chore", for the filtered person if there is one.
func (p choresPage) NewURL() string {
	v := url.Values{"back": {p.Back()}}
	if p.Person != nil {
		v.Set("assignee", strconv.FormatInt(p.Person.ID, 10))
	}
	return "/admin/chores/new?" + v.Encode()
}

// EditURL is a chore's page, coming back to this list.
func (p choresPage) EditURL(id int64) string {
	u := "/admin/chores/" + strconv.FormatInt(id, 10)
	if b := p.Back(); b != "/admin/chores" {
		u += "?" + url.Values{"back": {b}}.Encode()
	}
	return u
}

func (s *Server) handleChores(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Chores.Chores(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	people, err := s.Household.People(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byID := map[int64]*dbq.Person{}
	for i := range people {
		byID[people[i].ID] = &people[i]
	}
	page := choresPage{People: people, Q: parseChoresQuery(r.URL.Query(), people), Total: len(list)}
	page.Pool = page.Q.Person == personPool
	if id, err := strconv.ParseInt(page.Q.Person, 10, 64); err == nil {
		page.Person = byID[id]
	}
	page.Rows = make([]choreRow, 0, len(list))
	for _, c := range list {
		row := choreRow{Chore: c, Schedule: describeSchedule(chores.ParseRecurrence(c.Rrule), c.StartsOn)}
		if c.AssigneeID != nil {
			row.Assignee = byID[*c.AssigneeID]
		}
		switch {
		case page.Pool && c.AssigneeID != nil,
			page.Person != nil && (c.AssigneeID == nil || *c.AssigneeID != page.Person.ID):
			continue
		}
		page.Rows = append(page.Rows, row)
	}
	sortChoreRows(page.Rows, page.Q.Sort)
	v := s.view(r, "Chores", page)
	v.Nav = "chores"
	s.render(w, r, http.StatusOK, "admin_chores", v)
}

// sortChoreRows orders rows by sort: by points or money, high to low (the
// other amount, then the title, breaking ties), or leaves the title order.
func sortChoreRows(rows []choreRow, sort string) {
	if sort != sortPoints && sort != sortMoney {
		return
	}
	slices.SortStableFunc(rows, func(a, b choreRow) int {
		first, second := func(r choreRow) int64 { return r.Chore.Points }, func(r choreRow) int64 { return r.Chore.Pence }
		if sort == sortMoney {
			first, second = second, first
		}
		if d := cmp.Compare(first(b), first(a)); d != 0 {
			return d
		}
		if d := cmp.Compare(second(b), second(a)); d != 0 {
			return d
		}
		return cmp.Compare(a.Chore.Title, b.Chore.Title)
	})
}

// choreForm is the chore edit page.
type choreForm struct {
	ID               int64
	Title            string
	Emoji            emojiField
	Description      string
	Mode             string
	AssigneeID       int64
	Repeat           string
	Days             []time.Weekday
	MonthDay         int
	Custom           string // read-only RRULE for RepeatCustom
	StartsOn         string
	DueTime          string
	Points           int64
	Money            string
	RequiresApproval bool
	// Back is the chores list to return to, with its filter.
	Back string

	People    []dbq.Person
	WeekOrder []time.Weekday
	MonthDays []int
}

// HasDay is used by the template's day checkboxes.
func (f choreForm) HasDay(d time.Weekday) bool { return slices.Contains(f.Days, d) }

func (s *Server) newChoreForm(r *http.Request) (choreForm, error) {
	people, err := s.Household.People(r.Context(), s.HouseholdID)
	if err != nil {
		return choreForm{}, err
	}
	set, err := s.Settings.Get(r.Context(), s.HouseholdID)
	if err != nil {
		return choreForm{}, err
	}
	today := set.BoardDay(s.now())
	monthDays := make([]int, 28)
	for i := range monthDays {
		monthDays[i] = i + 1
	}
	return choreForm{
		Back: choreBack(r.URL.Query().Get("back"), people), // a POST reads it in readChoreForm
		Mode: chores.ModeAssigned, Repeat: chores.RepeatWeekly,
		Days: []time.Weekday{today.Weekday()}, MonthDay: today.Day(),
		StartsOn: today.Format(chores.DateLayout), Money: "0.00", Emoji: newEmojiField(emoji.Chore, ""),
		People: people, WeekOrder: chores.WeekOrder, MonthDays: monthDays,
	}, nil
}

func (s *Server) choreFormFor(r *http.Request, c dbq.Chore) (choreForm, error) {
	f, err := s.newChoreForm(r)
	if err != nil {
		return f, err
	}
	rec := chores.ParseRecurrence(c.Rrule)
	f.ID, f.Title, f.Description, f.Mode = c.ID, c.Title, c.Description, c.Mode
	f.Emoji.Value = c.Emoji
	if c.AssigneeID != nil {
		f.AssigneeID = *c.AssigneeID
	}
	f.Repeat, f.Days, f.Custom = rec.Kind, rec.Days, rec.Raw
	if rec.Kind == chores.RepeatMonthly {
		f.MonthDay = rec.MonthDay
	}
	f.StartsOn, f.Points, f.Money = c.StartsOn, c.Points, formatMoneyInput(c.Pence)
	if c.DueTime != nil {
		f.DueTime = *c.DueTime
	}
	f.RequiresApproval = c.RequiresApproval == 1
	return f, nil
}

func (s *Server) renderChore(w http.ResponseWriter, r *http.Request, status int, f choreForm, errs valid.Errors, flash string) {
	title := "Add a chore"
	if f.ID != 0 {
		title = f.Title
	}
	v := s.view(r, title, f)
	v.Nav = "chores"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_chore", v)
}

func (s *Server) handleChoreNew(w http.ResponseWriter, r *http.Request) {
	f, err := s.newChoreForm(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// "Add a chore for Alfie" from a filtered list.
	if id, err := strconv.ParseInt(r.URL.Query().Get("assignee"), 10, 64); err == nil {
		for _, p := range f.People {
			if p.ID == id {
				f.Mode, f.AssigneeID = chores.ModeAssigned, id
			}
		}
	}
	s.renderChore(w, r, http.StatusOK, f, nil, "")
}

func (s *Server) loadChore(w http.ResponseWriter, r *http.Request) (dbq.Chore, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return dbq.Chore{}, false
	}
	c, err := s.Chores.Chore(r.Context(), s.HouseholdID, id)
	if errors.Is(err, chores.ErrNotFound) || (err == nil && c.ArchivedAt != nil) {
		http.NotFound(w, r)
		return dbq.Chore{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return dbq.Chore{}, false
	}
	return c, true
}

func (s *Server) handleChoreEdit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChore(w, r)
	if !ok {
		return
	}
	f, err := s.choreFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderChore(w, r, http.StatusOK, f, nil, "")
}

// readChoreForm fills f from the request and returns the domain input.
// existing is the chore being edited (nil on create); a custom RRULE is only
// ever taken from it, never from the form.
func (s *Server) readChoreForm(w http.ResponseWriter, r *http.Request, f *choreForm, existing *dbq.Chore) (chores.Input, *form, error) {
	if err := parseForm(w, r); err != nil {
		return chores.Input{}, nil, err
	}
	fm := newForm(r)
	f.Title = fm.str("title")
	f.Emoji.read(fm)
	f.Description = fm.str("description")
	f.Mode = fm.str("mode")
	f.Repeat = fm.str("repeat")
	f.StartsOn = fm.str("starts_on")
	f.DueTime = fm.str("due_time")
	f.Points = fm.int("points", "Enter a whole number of points.")
	f.Money = fm.str("money")
	f.RequiresApproval = fm.checked("requires_approval")
	f.Days = nil
	for _, v := range r.Form["days"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6 {
			f.Days = append(f.Days, time.Weekday(n))
		}
	}
	if n, err := strconv.Atoi(fm.str("month_day")); err == nil {
		f.MonthDay = n
	}
	f.Back = choreBack(fm.str("back"), f.People)
	f.AssigneeID = 0
	if id := fm.optID("assignee_id"); id != nil {
		f.AssigneeID = *id
	}

	pence, err := s.Locale.ParseMoney(f.Money)
	if err != nil {
		fm.errs.Add("money", "Enter an amount like 0.50.")
	}
	in := chores.Input{
		Title: f.Title, Emoji: f.Emoji.Value, Description: f.Description, Mode: f.Mode,
		Repeat:   chores.Recurrence{Kind: f.Repeat, Days: f.Days, MonthDay: f.MonthDay},
		StartsOn: f.StartsOn, DueTime: fm.optStr("due_time"),
		Points: f.Points, Pence: pence, RequiresApproval: f.RequiresApproval,
	}
	if f.Mode == chores.ModeAssigned && f.AssigneeID != 0 {
		id := f.AssigneeID
		in.AssigneeID = &id
	}
	if f.Repeat == chores.RepeatCustom {
		if existing == nil || existing.Rrule == nil {
			fm.errs.Add("repeat", "Choose how often it repeats.")
		} else {
			in.Repeat.Raw = *existing.Rrule
			f.Custom = *existing.Rrule
		}
	}
	return in, fm, nil
}

func (s *Server) handleChoreCreate(w http.ResponseWriter, r *http.Request) {
	f, err := s.newChoreForm(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in, fm, err := s.readChoreForm(w, r, &f, nil)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !fm.ok() {
		s.renderChore(w, r, http.StatusUnprocessableEntity, f, fm.errs, "")
		return
	}
	if e, ok := f.Emoji.offerSuggestion(f.Title); ok {
		s.renderChore(w, r, http.StatusOK, f, nil, suggestionFlash(e, "chore"))
		return
	}
	_, err = s.Chores.CreateChore(r.Context(), s.HouseholdID, actor(r).Person.ID, in, s.boardDay(r))
	if errs, ok := validationErrors(err); ok {
		s.renderChore(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, f.Back)
}

func (s *Server) handleChoreUpdate(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadChore(w, r)
	if !ok {
		return
	}
	f, err := s.choreFormFor(r, cur)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in, fm, err := s.readChoreForm(w, r, &f, &cur)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !fm.ok() {
		s.renderChore(w, r, http.StatusUnprocessableEntity, f, fm.errs, "")
		return
	}
	if e, ok := f.Emoji.offerSuggestion(f.Title); ok {
		s.renderChore(w, r, http.StatusOK, f, nil, suggestionFlash(e, "chore"))
		return
	}
	c, err := s.Chores.UpdateChore(r.Context(), s.HouseholdID, actor(r).Person.ID, cur.ID, in, s.boardDay(r))
	if errs, ok := validationErrors(err); ok {
		s.renderChore(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	back := f.Back
	f, err = s.choreFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f.Back = back
	s.renderChore(w, r, http.StatusOK, f, nil, "Saved. Upcoming days have been updated.")
}

func (s *Server) handleChoreArchive(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChore(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.Chores.ArchiveChore(r.Context(), s.HouseholdID, actor(r).Person.ID, c.ID, s.boardDay(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	people, err := s.Household.People(r.Context(), s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, choreBack(r.PostFormValue("back"), people))
}

// boardDay is the household's current board day (after the reset time).
func (s *Server) boardDay(r *http.Request) time.Time {
	set, err := s.Settings.Get(r.Context(), s.HouseholdID)
	if err != nil {
		return s.now()
	}
	return set.BoardDay(s.now())
}

// describeSchedule summarises a schedule for the chores list.
func describeSchedule(rec chores.Recurrence, startsOn string) string {
	days := func() string {
		out := ""
		for _, d := range chores.WeekOrder {
			if slices.Contains(rec.Days, d) {
				if out != "" {
					out += ", "
				}
				out += d.String()[:3]
			}
		}
		return out
	}
	switch rec.Kind {
	case chores.RepeatOnce:
		if t, err := time.Parse(chores.DateLayout, startsOn); err == nil {
			return "Once, " + t.Format("02/01/2006")
		}
		return "Once"
	case chores.RepeatDaily:
		return "Every day"
	case chores.RepeatWeekdays:
		return "Weekdays"
	case chores.RepeatWeekly:
		return "Weekly: " + days()
	case chores.RepeatFortnightly:
		return "Every 2 weeks: " + days()
	case chores.RepeatMonthly:
		if rec.MonthDay == chores.LastDayOfMonth {
			return "Monthly, last day"
		}
		return "Monthly, day " + strconv.Itoa(rec.MonthDay)
	}
	return "Custom"
}
