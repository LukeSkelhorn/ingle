// Package locale formats dates and money according to the household's
// locale settings (UK by default: Europe/London, DD/MM/YYYY, Monday, £).
package locale

import (
	"strconv"
	"strings"
	"time"

	// Embed tzdata so Europe/London resolves in minimal container images.
	_ "time/tzdata"
)

// Locale holds the formatting preferences for a household.
type Locale struct {
	Location   *time.Location
	DateFormat string
	WeekStart  time.Weekday
	Currency   string
}

// UK returns the default UK locale.
func UK() Locale {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		// tzdata is embedded above, so this cannot fail.
		panic(err)
	}
	return Locale{
		Location:   loc,
		DateFormat: "02/01/2006",
		WeekStart:  time.Monday,
		Currency:   "GBP",
	}
}

// In converts t to the locale's timezone.
func (l Locale) In(t time.Time) time.Time {
	return t.In(l.Location)
}

// FormatDate formats t as a date in the locale's timezone (e.g. 03/11/2026).
func (l Locale) FormatDate(t time.Time) string {
	return l.In(t).Format(l.DateFormat)
}

// FormatTime formats t as a 24-hour clock time in the locale's timezone.
func (l Locale) FormatTime(t time.Time) string {
	return l.In(t).Format("15:04")
}

// StartOfWeek returns midnight (local time) on the first day of the week
// containing t.
func (l Locale) StartOfWeek(t time.Time) time.Time {
	t = l.In(t)
	offset := (int(t.Weekday()) - int(l.WeekStart) + 7) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d-offset, 0, 0, 0, 0, l.Location)
}

var currencySymbols = map[string]string{
	"GBP": "£",
	"EUR": "€",
	"USD": "$",
}

// FormatMoney formats an amount in minor units (pence) e.g. 150 -> "£1.50".
func (l Locale) FormatMoney(minor int64) string {
	var b strings.Builder
	abs := uint64(minor) //nolint:gosec // deliberate two's-complement reinterpretation; sign handled below
	if minor < 0 {
		b.WriteByte('-')
		abs = -abs // unsigned negation is exact even for MinInt64
	}
	return formatAbs(&b, l.Currency, abs)
}

func formatAbs(b *strings.Builder, currency string, abs uint64) string {
	sym, ok := currencySymbols[currency]
	if !ok {
		sym = currency + " "
	}
	b.WriteString(sym)
	b.WriteString(groupThousands(strconv.FormatUint(abs/100, 10)))
	b.WriteByte('.')
	pence := abs % 100
	if pence < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.FormatUint(pence, 10))
	return b.String()
}

func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
