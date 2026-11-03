// Package locale formats dates and money according to the household's
// locale settings (UK by default: Europe/London, DD/MM/YYYY, Monday, £).
package locale

import (
	"errors"
	"fmt"
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

// ErrInvalidMoney is returned by ParseMoney for input it can't read.
var ErrInvalidMoney = errors.New("enter an amount like 1.50")

// ParseMoney reads an amount typed into a form ("1.50", "£1.50", "0.5",
// "2", "1,000.00") as minor units (pence). Negative amounts and more than
// two decimal places are rejected; an empty string is zero.
func (l Locale) ParseMoney(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if sym, ok := currencySymbols[l.Currency]; ok {
		s = strings.TrimPrefix(s, sym)
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, ErrInvalidMoney
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if hasFrac && (len(frac) == 0 || len(frac) > 2) {
		return 0, ErrInvalidMoney
	}
	for len(frac) < 2 {
		frac += "0"
	}
	if !allDigits(whole) || !allDigits(frac) || len(whole) > 12 {
		return 0, ErrInvalidMoney
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, ErrInvalidMoney
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, ErrInvalidMoney
	}
	return w*100 + f, nil
}

// FormatMoneyInput formats pence for a form field, without the symbol or
// thousands separators (150 -> "1.50").
func FormatMoneyInput(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
