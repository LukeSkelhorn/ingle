// Package config loads Domus runtime configuration from the environment,
// falling back to UK-centric defaults.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	// Embed the IANA timezone database so Europe/London resolves even in
	// minimal container images without /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Defaults used when the corresponding DOMUS_* variable is unset.
const (
	DefaultAddr       = ":8080"
	DefaultDBPath     = "./data/domus.db"
	DefaultTimezone   = "Europe/London"
	DefaultDateFormat = "02/01/2006" // DD/MM/YYYY
	DefaultWeekStart  = "monday"
	DefaultCurrency   = "GBP"
)

// Config is the resolved runtime configuration.
type Config struct {
	Addr       string
	DBPath     string
	Location   *time.Location
	DateFormat string
	WeekStart  time.Weekday
	Currency   string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	tz := get("DOMUS_TZ", DefaultTimezone)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Config{}, fmt.Errorf("DOMUS_TZ %q: %w", tz, err)
	}

	ws, err := parseWeekday(get("DOMUS_WEEK_START", DefaultWeekStart))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:       get("DOMUS_ADDR", DefaultAddr),
		DBPath:     get("DOMUS_DB_PATH", DefaultDBPath),
		Location:   loc,
		DateFormat: get("DOMUS_DATE_FORMAT", DefaultDateFormat),
		WeekStart:  ws,
		Currency:   strings.ToUpper(get("DOMUS_CURRENCY", DefaultCurrency)),
	}, nil
}

func parseWeekday(s string) (time.Weekday, error) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String(), s) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("DOMUS_WEEK_START %q: not a weekday name", s)
}
