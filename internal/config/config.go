// Package config loads Ingle runtime configuration from the environment,
// falling back to UK-centric defaults.
//
// Settings are INGLE_* variables. Ingle used to be called Domus, so the
// old DOMUS_* names are still read when the INGLE_* one isn't set, and an
// existing domus.db is still used.
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// Embed the IANA timezone database so Europe/London resolves even in
	// minimal container images without /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Variable prefixes: settings are INGLE_*, and the DOMUS_* names from
// before the rename are read as a fallback.
const (
	Prefix       = "INGLE_"
	LegacyPrefix = "DOMUS_"
)

// Database file names. A data directory from before the rename has a
// domus.db, which is used as long as there's no ingle.db beside it.
const (
	DBName       = "ingle.db"
	LegacyDBName = "domus.db"
)

// Defaults used when the corresponding INGLE_* variable is unset.
const (
	DefaultAddr       = ":8080"
	DefaultDBPath     = "./data/" + DBName
	DefaultTimezone   = "Europe/London"
	DefaultDateFormat = "02/01/2006" // DD/MM/YYYY
	DefaultWeekStart  = "monday"
	DefaultCurrency   = "GBP"
	DefaultBackupKeep = 14
	DefaultBackupAt   = "03:00"
)

// Config is the resolved runtime configuration.
type Config struct {
	Addr   string
	DBPath string
	// DatabaseURL, when set, is a postgres:// URL used instead of the
	// SQLite file at DBPath (Hosted). DBPath's folder still holds avatars.
	DatabaseURL string
	// DatabaseMigrateURL is the tables' owner, which runs migrations, when
	// DatabaseURL is a role that only reads and writes rows. Empty means
	// DatabaseURL runs them.
	DatabaseMigrateURL string
	Location           *time.Location
	DateFormat         string
	WeekStart          time.Weekday
	Currency           string
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
	// Legacy lists the old DOMUS_* variables that were used because the
	// INGLE_* one wasn't set, so the server can suggest renaming them.
	Legacy []string
}

// fileExists reports whether path is an existing file. A variable so tests
// can stand in for the filesystem.
var fileExists = func(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	var legacy []string
	// get reads INGLE_<name>, then DOMUS_<name>, then falls back to def.
	get := func(name, def string) string {
		if v := strings.TrimSpace(getenv(Prefix + name)); v != "" {
			return v
		}
		if v := strings.TrimSpace(getenv(LegacyPrefix + name)); v != "" {
			legacy = append(legacy, LegacyPrefix+name)
			return v
		}
		return def
	}

	tz := get("TZ", DefaultTimezone)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Config{}, fmt.Errorf(Prefix+"TZ %q: %w", tz, err)
	}

	ws, err := parseWeekday(get("WEEK_START", DefaultWeekStart))
	if err != nil {
		return Config{}, err
	}

	proxies, err := parsePrefixes(get("TRUSTED_PROXIES", ""))
	if err != nil {
		return Config{}, err
	}

	dbURL := get("DATABASE_URL", "")
	if dbURL != "" && !strings.HasPrefix(dbURL, "postgres://") && !strings.HasPrefix(dbURL, "postgresql://") {
		return Config{}, fmt.Errorf(Prefix + "DATABASE_URL: want a postgres:// URL")
	}

	migrateURL := get("DATABASE_MIGRATE_URL", "")
	if migrateURL != "" && dbURL == "" {
		return Config{}, fmt.Errorf(Prefix + "DATABASE_MIGRATE_URL needs " + Prefix + "DATABASE_URL")
	}

	defKeep := strconv.Itoa(DefaultBackupKeep)
	if dbURL != "" {
		// Backups copy the SQLite file. On Postgres, the database service
		// backs up (point-in-time recovery).
		defKeep = "0"
	}
	keepS := get("BACKUP_KEEP", defKeep)
	keep, err := strconv.Atoi(keepS)
	if err != nil || keep < 0 || keep > 365 {
		return Config{}, fmt.Errorf(Prefix+"BACKUP_KEEP %q: want a number from 0 to 365", keepS)
	}
	if dbURL != "" && keep > 0 {
		return Config{}, fmt.Errorf(Prefix + "BACKUP_KEEP: backups are for SQLite; with " + Prefix + "DATABASE_URL, leave it unset or 0 and use the database's own backups")
	}
	at := get("BACKUP_AT", DefaultBackupAt)
	if _, err := time.Parse("15:04", at); err != nil || len(at) != 5 {
		return Config{}, fmt.Errorf(Prefix+"BACKUP_AT %q: want HH:MM", at)
	}

	dbPath := legacyDB(get("DB_PATH", DefaultDBPath))
	return Config{
		Addr:               get("ADDR", DefaultAddr),
		DBPath:             dbPath,
		DatabaseURL:        dbURL,
		DatabaseMigrateURL: migrateURL,
		Location:           loc,
		DateFormat:         get("DATE_FORMAT", DefaultDateFormat),
		WeekStart:          ws,
		Currency:           strings.ToUpper(get("CURRENCY", DefaultCurrency)),
		PhotosDir:          get("PHOTOS_DIR", ""),
		TrustedProxies:     proxies,
		BackupDir:          get("BACKUP_DIR", filepath.Join(filepath.Dir(dbPath), "backups")),
		BackupKeep:         keep,
		BackupAt:           at,
		Legacy:             legacy,
	}, nil
}

// DSN is what store.Open opens: the Postgres URL if set, else the SQLite
// file.
func (c Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return c.DBPath
}

// DBLabel names the database for logs without its password.
func (c Config) DBLabel() string {
	if c.DatabaseURL == "" {
		return c.DBPath
	}
	if u, err := url.Parse(c.DatabaseURL); err == nil {
		return u.Redacted()
	}
	return "postgres"
}

// legacyDB keeps an install from before the rename on its database: if
// path is an ingle.db that doesn't exist yet but a domus.db does beside
// it, that's the one to open. Otherwise path is used as given.
func legacyDB(path string) string {
	if filepath.Base(path) != DBName || fileExists(path) {
		return path
	}
	if old := filepath.Join(filepath.Dir(path), LegacyDBName); fileExists(old) {
		return old
	}
	return path
}

// parsePrefixes reads a comma- or space-separated list of CIDRs; a bare IP
// means just that address.
func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf(Prefix+"TRUSTED_PROXIES %q: %w", f, err)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf(Prefix+"TRUSTED_PROXIES %q: %w", f, err)
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
	return 0, fmt.Errorf(Prefix+"WEEK_START %q: not a weekday name", s)
}
