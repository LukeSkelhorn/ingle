// Package config loads Domus runtime configuration from the environment,
// falling back to UK-centric defaults.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
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
	DefaultBackupKeep = 14
	DefaultBackupAt   = "03:00"
)

// Config is the resolved runtime configuration.
type Config struct {
	Addr       string
	DBPath     string
	Location   *time.Location
	DateFormat string
	WeekStart  time.Weekday
	Currency   string
	// PhotosDir is an optional read-only folder of photos (e.g. a NAS
	// share) mixed into the screensaver slideshow. Empty means none.
	PhotosDir string
	// TrustedProxies are the reverse proxies (e.g. Caddy) whose
	// X-Forwarded-For header is believed. Empty means none: the TCP peer
	// is the client.
	TrustedProxies []netip.Prefix
	// BackupDir receives the nightly backups; BackupKeep is how many are
	// kept (0 turns backups off) and BackupAt the household-time "HH:MM"
	// they run.
	BackupDir  string
	BackupKeep int
	BackupAt   string
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

	proxies, err := parsePrefixes(get("DOMUS_TRUSTED_PROXIES", ""))
	if err != nil {
		return Config{}, err
	}

	keep, err := strconv.Atoi(get("DOMUS_BACKUP_KEEP", strconv.Itoa(DefaultBackupKeep)))
	if err != nil || keep < 0 || keep > 365 {
		return Config{}, fmt.Errorf("DOMUS_BACKUP_KEEP %q: want a number from 0 to 365", getenv("DOMUS_BACKUP_KEEP"))
	}
	at := get("DOMUS_BACKUP_AT", DefaultBackupAt)
	if _, err := time.Parse("15:04", at); err != nil || len(at) != 5 {
		return Config{}, fmt.Errorf("DOMUS_BACKUP_AT %q: want HH:MM", at)
	}

	dbPath := get("DOMUS_DB_PATH", DefaultDBPath)
	return Config{
		Addr:           get("DOMUS_ADDR", DefaultAddr),
		DBPath:         dbPath,
		Location:       loc,
		DateFormat:     get("DOMUS_DATE_FORMAT", DefaultDateFormat),
		WeekStart:      ws,
		Currency:       strings.ToUpper(get("DOMUS_CURRENCY", DefaultCurrency)),
		PhotosDir:      get("DOMUS_PHOTOS_DIR", ""),
		TrustedProxies: proxies,
		BackupDir:      get("DOMUS_BACKUP_DIR", filepath.Join(filepath.Dir(dbPath), "backups")),
		BackupKeep:     keep,
		BackupAt:       at,
	}, nil
}

// parsePrefixes reads a comma- or space-separated list of CIDRs; a bare IP
// means just that address.
func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("DOMUS_TRUSTED_PROXIES %q: %w", f, err)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("DOMUS_TRUSTED_PROXIES %q: %w", f, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func parseWeekday(s string) (time.Weekday, error) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String(), s) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("DOMUS_WEEK_START %q: not a weekday name", s)
}
