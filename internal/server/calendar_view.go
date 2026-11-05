package server

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/lukeskelhorn/domus/internal/calendar"
	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// The board calendar's reach: weeks back from this week, and weeks (or
// months) ahead. The calendar feeds cache enough to cover it.
const (
	calWeeksBack   = 4
	calWeeksAhead  = 13
	calMonthsAhead = 2
	// calMonthItems is how many items a month cell lists before "+N more".
	calMonthItems = 3
)

// Calendar views.
const (
	calWeek  = "week"
	calMonth = "month"
)

// calView is the board calendar: a week or a month grid.
type calView struct {
	View  string // calWeek or calMonth
	Title string // "3–9 Nov" or "November 2026"
	// Prev, Next and Today are the toolbar's URLs; Prev and Next are ""
	// at the edges of the range.
	Prev, Next, Today string
	Self              string // this view, for refreshing in place
	Week, Month       string // the same range in the other view
	ShowsToday        bool
	Weekdays          []string // column headings, from the week start
	Weeks             [][]calDay
	HasCalendars      bool
}

// calDay is one day of the grid.
type calDay struct {
	Date    string // YYYY-MM-DD
	Num     int
	Label   string // "Mon 3 Nov"
	Today   bool
	Past    bool
	Outside bool // in a month grid, a day of the month before or after
	AllDay  []calItem
	Items   []calItem // timed events and chores, in time order
	// Shown and More are the month cell: the first few items and how many
	// didn't fit.
	Shown []calItem
	More  int
}

// calItem is an event or a chore on a day.
type calItem struct {
	Title  string
	When   string // "09:00", "until 10:00", "All day" or "" for an untimed chore
	Colour string // the feed's colour, or the person's for a chore
	Feed   string
	Chore  *chores.CalChore
	Person *dbq.Person // whose chore it is; nil for one up for grabs
	DoneBy *dbq.Person
	sort   string
}

// IsChore reports whether the item is a chore (else it's an event).
func (i calItem) IsChore() bool { return i.Chore != nil }

// Count is the day's number of items, for the month view's dots.
func (d calDay) Count() int { return len(d.AllDay) + len(d.Items) }

// calendarLocale is the household locale in its own timezone.
func (s *Server) calendarLocale(r *http.Request) locale.Locale {
	loc := s.Locale
	loc.Location = s.Settings.Location(r.Context(), s.HouseholdID)
	return loc
}

// calRange works out the anchor (week start or 1st of the month) for the
// requested view and date, clamped to the calendar's reach.
type calRange struct {
	view        string
	anchor      time.Time
	first, last time.Time // anchor limits
	gridFrom    time.Time // first day shown
	gridTo      time.Time // last day shown
	today       time.Time
}

func (s *Server) calRange(r *http.Request, loc locale.Locale) calRange {
	today := s.boardDay(r)
	thisWeek := loc.StartOfWeek(today)
	cr := calRange{view: calWeek, today: today}
	if r.URL.Query().Get("view") == calMonth {
		cr.view = calMonth
	}
	from := today
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.ParseInLocation(chores.DateLayout, v, loc.Location); err == nil {
			from = t
		}
	}
	switch cr.view {
	case calMonth:
		back := thisWeek.AddDate(0, 0, -7*calWeeksBack)
		cr.first = time.Date(back.Year(), back.Month(), 1, 0, 0, 0, 0, loc.Location)
		cr.last = time.Date(today.Year(), today.Month()+calMonthsAhead, 1, 0, 0, 0, 0, loc.Location)
		cr.anchor = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, loc.Location)
	default:
		cr.first = thisWeek.AddDate(0, 0, -7*calWeeksBack)
		cr.last = thisWeek.AddDate(0, 0, 7*calWeeksAhead)
		cr.anchor = loc.StartOfWeek(from)
	}
	if cr.anchor.Before(cr.first) {
		cr.anchor = cr.first
	}
	if cr.anchor.After(cr.last) {
		cr.anchor = cr.last
	}
	cr.gridFrom = loc.StartOfWeek(cr.anchor)
	cr.gridTo = cr.gridFrom.AddDate(0, 0, 6)
	if cr.view == calMonth {
		monthEnd := cr.anchor.AddDate(0, 1, -1)
		cr.gridTo = loc.StartOfWeek(monthEnd).AddDate(0, 0, 6)
	}
	return cr
}

func calURL(view string, t time.Time) string {
	return "/board/calendar?" + url.Values{"view": {view}, "from": {t.Format(chores.DateLayout)}}.Encode()
}

func (s *Server) handleCalendarView(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	cr := s.calRange(r, loc)
	days, err := s.calendarDays(r, loc, cr.gridFrom, cr.gridTo, cr.today)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	v := calView{View: cr.view, Today: calURL(cr.view, cr.today), Self: calURL(cr.view, cr.anchor)}
	v.Week = calURL(calWeek, cr.anchor)
	v.Month = calURL(calMonth, cr.anchor)
	if cr.view == calMonth && cr.anchor.Year() == cr.today.Year() && cr.anchor.Month() == cr.today.Month() {
		v.Week = calURL(calWeek, cr.today)
	}
	step := func(n int) time.Time {
		if cr.view == calMonth {
			return cr.anchor.AddDate(0, n, 0)
		}
		return cr.anchor.AddDate(0, 0, 7*n)
	}
	if p := step(-1); !p.Before(cr.first) {
		v.Prev = calURL(cr.view, p)
	}
	if n := step(1); !n.After(cr.last) {
		v.Next = calURL(cr.view, n)
	}
	for i := range 7 {
		v.Weekdays = append(v.Weekdays, cr.gridFrom.AddDate(0, 0, i).Format("Mon"))
	}
	for i := 0; i < len(days); i += 7 {
		week := days[i:min(i+7, len(days))]
		for j := range week {
			d := &week[j]
			if cr.view == calMonth {
				t, _ := time.ParseInLocation(chores.DateLayout, d.Date, loc.Location)
				d.Outside = t.Month() != cr.anchor.Month()
				all := append(slices.Clone(d.AllDay), d.Items...)
				d.Shown = all[:min(len(all), calMonthItems)]
				d.More = len(all) - len(d.Shown)
			}
			if d.Today && !d.Outside {
				v.ShowsToday = true
			}
		}
		v.Weeks = append(v.Weeks, week)
	}
	if cr.view == calMonth {
		v.Title = cr.anchor.Format("January 2006")
	} else {
		v.Title = weekTitle(cr.gridFrom, cr.gridTo)
	}
	if s.Calendars != nil {
		feeds, err := s.Calendars.Feeds(r.Context(), s.HouseholdID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		v.HasCalendars = len(feeds) > 0
	}
	s.renderPartial(w, r, "calendar", v)
}

// weekTitle is "3–9 Nov", "30 Nov – 6 Dec" or "29 Dec 2026 – 4 Jan 2027".
func weekTitle(from, to time.Time) string {
	switch {
	case from.Year() != to.Year():
		return from.Format("2 Jan 2006") + " – " + to.Format("2 Jan 2006")
	case from.Month() != to.Month():
		return from.Format("2 Jan") + " – " + to.Format("2 Jan")
	}
	return strconv.Itoa(from.Day()) + "–" + to.Format("2 Jan")
}

// calendarDays builds each day from first to last (local midnights) with
// its events and chores.
func (s *Server) calendarDays(r *http.Request, loc locale.Locale, first, last, today time.Time) ([]calDay, error) {
	ctx := r.Context()
	z := loc.Location
	from, to := first.Format(chores.DateLayout), last.Format(chores.DateLayout)
	cal, err := s.Chores.Calendar(ctx, s.HouseholdID, today, from, to)
	if err != nil {
		return nil, err
	}
	people, err := s.Store.Q.ListPeople(ctx, s.HouseholdID)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*dbq.Person, len(people))
	for i := range people {
		byID[people[i].ID] = &people[i]
	}
	var events []calendar.Event
	if s.Calendars != nil {
		events = s.Calendars.Upcoming(s.HouseholdID, first, last.AddDate(0, 0, 1))
	}

	var days []calDay
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		date := d.Format(chores.DateLayout)
		next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, z)
		day := calDay{
			Date: date, Num: d.Day(), Label: d.Format("Mon 2 Jan"),
			Today: date == today.Format(chores.DateLayout), Past: d.Before(today),
		}
		for _, e := range events {
			if !e.Start.Before(next) || !e.End.After(d) {
				continue
			}
			it := calItem{Title: e.Title, Colour: e.Colour, Feed: e.Feed}
			start, end := e.Start.In(z), e.End.In(z)
			switch {
			case e.AllDay, start.Before(d) && !end.Before(next):
				it.When = "All day"
				day.AllDay = append(day.AllDay, it)
				continue
			case start.Before(d):
				it.When, it.sort = "until "+loc.FormatTime(end), "00:00"
			default:
				it.When = loc.FormatTime(start)
				it.sort = it.When
			}
			day.Items = append(day.Items, it)
		}
		for i := range cal[date] {
			c := &cal[date][i]
			it := calItem{Title: c.Title, Chore: c, sort: "~"} // untimed chores last
			if c.DueTime != nil {
				it.When, it.sort = *c.DueTime, *c.DueTime
			}
			if c.PersonID != nil {
				it.Person = byID[*c.PersonID]
			}
			if c.DoneByID != nil {
				it.DoneBy = byID[*c.DoneByID]
			}
			if it.Person != nil {
				it.Colour = it.Person.Colour
			}
			day.Items = append(day.Items, it)
		}
		slices.SortStableFunc(day.Items, func(a, b calItem) int { return cmp.Compare(a.sort, b.sort) })
		days = append(days, day)
	}
	return days, nil
}

// calDaySheet is the sheet listing one day.
type calDaySheet struct {
	Day          calDay
	HasCalendars bool
}

func (s *Server) handleCalendarDay(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	d, ok := s.calDate(r, loc)
	if !ok {
		http.NotFound(w, r)
		return
	}
	days, err := s.calendarDays(r, loc, d, d, s.boardDay(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "cal_day", calDaySheet{Day: days[0]})
}

// calChoreSheet is the read-only sheet for a chore on the calendar.
type calChoreSheet struct {
	Item   calItem
	Day    string // "Tuesday 3 November"
	Repeat string
	DoneAt string
	Chore  dbq.Chore
}

func (s *Server) handleCalendarChore(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	id, ok := pathID(r)
	d, dok := s.calDate(r, loc)
	if !ok || !dok {
		http.NotFound(w, r)
		return
	}
	date := d.Format(chores.DateLayout)
	c, ch, err := s.Chores.ChoreOn(r.Context(), s.HouseholdID, s.boardDay(r), id, date)
	if errors.Is(err, chores.ErrNotFound) {
		s.boardFailed(w, r, "That chore isn't on the calendar any more.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	people, err := s.Store.Q.ListPeople(r.Context(), s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := calChoreSheet{
		Item:   calItem{Title: c.Title, Chore: &c},
		Day:    d.Format("Monday 2 January"),
		Repeat: describeSchedule(chores.ParseRecurrence(ch.Rrule), ch.StartsOn),
		Chore:  ch,
	}
	if c.DueTime != nil {
		v.Item.When = *c.DueTime
	}
	for i := range people {
		p := &people[i]
		if c.PersonID != nil && p.ID == *c.PersonID {
			v.Item.Person, v.Item.Colour = p, p.Colour
		}
		if c.DoneByID != nil && p.ID == *c.DoneByID {
			v.Item.DoneBy = p
		}
	}
	if c.DoneAt != 0 {
		v.DoneAt = loc.FormatTime(time.Unix(c.DoneAt, 0))
	}
	s.renderPartial(w, r, "cal_chore", v)
}

// calDate reads the {date} path value as a local midnight within the
// calendar's reach (with room for a month grid's edges).
func (s *Server) calDate(r *http.Request, loc locale.Locale) (time.Time, bool) {
	d, err := time.ParseInLocation(chores.DateLayout, r.PathValue("date"), loc.Location)
	if err != nil {
		return time.Time{}, false
	}
	today := s.boardDay(r)
	if d.Before(today.AddDate(0, 0, -7*(calWeeksBack+6))) || d.After(today.AddDate(0, calMonthsAhead+1, 7)) {
		return time.Time{}, false
	}
	return d, true
}
