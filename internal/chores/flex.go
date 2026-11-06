package chores

import (
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Day modes: how a chore's scheduled days work.
const (
	// DayEach is due on every day of the schedule (the default).
	DayEach = "each"
	// DayAny is due once a week (Mon–Sun), on whichever of the scheduled
	// days suits: a flexible-day chore.
	DayAny = "any"
)

// AnyDayAllowed reports whether a repeat kind can be a flexible-day chore.
func AnyDayAllowed(kind string) bool {
	switch kind {
	case RepeatWeekly, RepeatFortnightly, RepeatWeekdays:
		return true
	}
	return false
}

// flexWindow is one week of a flexible-day chore: the allowed dates, first
// to last. It becomes one occurrence, available from Days[0] and due on the
// last.
type flexWindow struct {
	Days []string // YYYY-MM-DD, ascending
}

func (w flexWindow) From() string { return w.Days[0] }
func (w flexWindow) Due() string  { return w.Days[len(w.Days)-1] }

// weekOf returns the Monday of date's Mon–Sun week. Flexible-day windows
// always run Monday to Sunday, whatever the household's week start, so a
// Sat+Sun chore stays one weekend.
func weekOf(date string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	back := (int(t.Weekday()) + 6) % 7 // days since Monday
	return t.AddDate(0, 0, -back).Format(DateLayout)
}

// mondayOf returns the start of t's Mon–Sun week, at local midnight.
func mondayOf(t time.Time) time.Time {
	back := (int(t.Weekday()) + 6) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d-back, 0, 0, 0, 0, t.Location())
}

// sundayAfter returns the end of t's Mon–Sun week, at local midnight.
func sundayAfter(t time.Time) time.Time {
	ahead := (7 - int(t.Weekday())) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d+ahead, 0, 0, 0, 0, t.Location())
}

// groupWeeks splits ascending dates into Mon–Sun windows.
func groupWeeks(dates []string) []flexWindow {
	var out []flexWindow
	for _, d := range dates {
		if n := len(out); n > 0 && weekOf(out[n-1].Due()) == weekOf(d) {
			out[n-1].Days = append(out[n-1].Days, d)
			continue
		}
		out = append(out, flexWindow{Days: []string{d}})
	}
	return out
}

// flexWindows returns the windows of flexible-day chore c that start in
// [start, end]. A window that runs past end is followed to its Sunday, so it
// always gets its real last day; days before start are left out, so a chore
// created or edited mid-week starts this week from start.
func flexWindows(c dbq.Chore, start, end time.Time, loc *time.Location) ([]flexWindow, error) {
	dates, err := occurrenceDates(c, start, sundayAfter(end), loc)
	if err != nil {
		return nil, err
	}
	ws := groupWeeks(dates)
	last := end.Format(DateLayout)
	for len(ws) > 0 && ws[len(ws)-1].From() > last {
		ws = ws[:len(ws)-1]
	}
	return ws, nil
}

// windowDays returns the allowed days of a flexible-day occurrence in
// [from, due] (YYYY-MM-DD), from its chore's schedule.
func windowDays(rrule *string, startsOn, from, due string, loc *time.Location) []string {
	first, err1 := time.ParseInLocation(DateLayout, from, loc)
	last, err2 := time.ParseInLocation(DateLayout, due, loc)
	if err1 != nil || err2 != nil {
		return []string{due}
	}
	dates, err := occurrenceDates(dbq.Chore{Rrule: rrule, StartsOn: startsOn}, first, last, loc)
	if err != nil || len(dates) == 0 {
		return []string{due}
	}
	return dates
}

// shortDay is "Mon", "Tue", … for a YYYY-MM-DD date.
func shortDay(date string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	return t.Format("Mon")
}

// orList joins words as "a, b or c".
func orList(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// FlexLabel is the board label for a flexible-day chore on today, given the
// days it can still be done (today first): "Today or Thu", "Today, Wed or
// Thu", or "Any day until Fri" for four or more. It is "" when today is the
// only day left.
func FlexLabel(today string, days []string) string {
	if len(days) < 2 || days[0] != today {
		return ""
	}
	if len(days) >= 4 {
		return "Any day until " + shortDay(days[len(days)-1])
	}
	words := []string{"Today"}
	for _, d := range days[1:] {
		words = append(words, shortDay(d))
	}
	return orList(words)
}

// DaysHint names a window's days, "Tue or Thu"; five weekdays in a row are
// "Mon–Fri".
func DaysHint(days []string) string {
	words := make([]string, len(days))
	for i, d := range days {
		words[i] = shortDay(d)
	}
	if strings.Join(words, ",") == "Mon,Tue,Wed,Thu,Fri" {
		return "Mon–Fri"
	}
	return orList(words)
}
