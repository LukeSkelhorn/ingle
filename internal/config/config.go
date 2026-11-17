// Package config loads Ingle runtime configuration from the environment, falling back to UK-centric defaults.
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

	"github.com/lukeskelhorn/ingle/internal/secrets"

	// Minimal container images have no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Variable prefixes: INGLE_*, with the pre-rename DOMUS_* names as a fallback.
const (
	Prefix       = "INGLE_"
	LegacyPrefix = "DOMUS_"
)

// Database file names.
const (
	DBName       = "ingle.db"
	LegacyDBName = "domus.db"
)

// Defaults used when the corresponding INGLE_* variable is unset.
const (
	DefaultAddr       = ":8080"
	DefaultDBPath     = "./data/" + DBName
	DefaultTimezone   = "Europe/London"
	DefaultDateFormat = "02/01/2006"
	DefaultWeekStart  = "monday"
	DefaultCurrency   = "GBP"
	DefaultBackupKeep = 14
	DefaultBackupAt   = "03:00"
)

// Config is the resolved runtime configuration.
type Config struct {
	Addr               string
	DBPath             string
	DatabaseURL        string
	DatabaseMigrateURL string
	MasterKey          []byte
	Location           *time.Location
	DateFormat         string
	WeekStart          time.Weekday
	Currency           string
	PhotosDir          string
	TrustedProxies     []netip.Prefix
	BackupDir          string
	BackupKeep         int
	BackupAt           string
	Legacy             []string
}

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

	var masterKey []byte
	if v := get("MASTER_KEY", ""); v != "" {
		if masterKey, err = secrets.ParseMasterKey(v); err != nil {
			return Config{}, fmt.Errorf(Prefix+"MASTER_KEY: %w", err)
		}
	} else if dbURL != "" {
		return Config{}, fmt.Errorf(Prefix + "MASTER_KEY: with " + Prefix + "DATABASE_URL, set a key to encrypt households' secrets (make one with: openssl rand -base64 32)")
	}

	defKeep := strconv.Itoa(DefaultBackupKeep)
	if dbURL != "" {
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
		MasterKey:          masterKey,
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

// DSN is what store.Open opens: the Postgres URL if set, else the SQLite file.
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

func legacyDB(path string) string {
	if filepath.Base(path) != DBName || fileExists(path) {
		return path
	}
	if old := filepath.Join(filepath.Dir(path), LegacyDBName); fileExists(old) {
		return old
	}
	return path
}

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
