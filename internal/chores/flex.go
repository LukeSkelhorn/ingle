package chores

import (
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

const (
	// DayEach is due on every day of the schedule (the default).
	DayEach = "each"
	// DayAny is due once a week, on whichever of the scheduled days suits.
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

type flexWindow struct {
	Days []string
}

func (w flexWindow) From() string { return w.Days[0] }
func (w flexWindow) Due() string  { return w.Days[len(w.Days)-1] }

// weekOf is always Mon–Sun, whatever the household's week start, so a Sat+Sun chore stays one weekend.
func weekOf(date string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	back := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -back).Format(DateLayout)
}

func mondayOf(t time.Time) time.Time {
	back := (int(t.Weekday()) + 6) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d-back, 0, 0, 0, 0, t.Location())
}

func sundayAfter(t time.Time) time.Time {
	ahead := (7 - int(t.Weekday())) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d+ahead, 0, 0, 0, 0, t.Location())
}

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

func shortDay(date string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	return t.Format("Mon")
}

func orList(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// FlexLabel is the board label for a flexible-day chore, e.g. "Today or Thu".
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

// DaysHint names a window's days, "Tue or Thu"; five weekdays in a row are "Mon–Fri".
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
