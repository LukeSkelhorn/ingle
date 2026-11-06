package config

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(func(string) string { return "" })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if cfg.Location.String() != "Europe/London" {
		t.Errorf("Location = %q, want Europe/London", cfg.Location)
	}
	if cfg.DateFormat != "02/01/2006" {
		t.Errorf("DateFormat = %q", cfg.DateFormat)
	}
	if cfg.WeekStart != time.Monday {
		t.Errorf("WeekStart = %v, want Monday", cfg.WeekStart)
	}
	if cfg.Currency != "GBP" {
		t.Errorf("Currency = %q, want GBP", cfg.Currency)
	}
	if cfg.PhotosDir != "" {
		t.Errorf("PhotosDir = %q, want none", cfg.PhotosDir)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none", cfg.TrustedProxies)
	}
	if cfg.BackupDir != "data/backups" || cfg.BackupKeep != 14 || cfg.BackupAt != "03:00" {
		t.Errorf("backup = %q %d %q", cfg.BackupDir, cfg.BackupKeep, cfg.BackupAt)
	}
}

func TestLoadBackupAndProxies(t *testing.T) {
	env := map[string]string{
		"INGLE_DB_PATH":         "/data/ingle.db",
		"INGLE_TRUSTED_PROXIES": "172.30.0.0/24, 10.0.0.5",
		"INGLE_BACKUP_KEEP":     "0",
		"INGLE_BACKUP_AT":       "23:30",
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fmt.Sprint(cfg.TrustedProxies); got != "[172.30.0.0/24 10.0.0.5/32]" {
		t.Errorf("TrustedProxies = %s", got)
	}
	if cfg.BackupDir != "/data/backups" || cfg.BackupKeep != 0 || cfg.BackupAt != "23:30" {
		t.Errorf("backup = %q %d %q", cfg.BackupDir, cfg.BackupKeep, cfg.BackupAt)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"INGLE_ADDR":       "127.0.0.1:9000",
		"INGLE_TZ":         "Europe/Dublin",
		"INGLE_WEEK_START": "Sunday",
		"INGLE_CURRENCY":   "eur",
		"INGLE_PHOTOS_DIR": "/photos",
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.Location.String() != "Europe/Dublin" ||
		cfg.WeekStart != time.Sunday || cfg.Currency != "EUR" || cfg.PhotosDir != "/photos" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	for _, tc := range []map[string]string{
		{"INGLE_TZ": "Not/AZone"},
		{"INGLE_WEEK_START": "funday"},
		{"INGLE_TRUSTED_PROXIES": "caddy"},
		{"INGLE_BACKUP_KEEP": "-1"},
		{"INGLE_BACKUP_AT": "3am"},
	} {
		if _, err := load(func(k string) string { return tc[k] }); err == nil {
			t.Errorf("load(%v) succeeded, want error", tc)
		}
	}
}

// Ingle used to be called Domus: its DOMUS_* settings still work, and an
// INGLE_* one wins when both are set.
func TestLoadLegacyNames(t *testing.T) {
	env := map[string]string{
		"DOMUS_TZ":       "Europe/Dublin",
		"DOMUS_CURRENCY": "eur",
		"INGLE_CURRENCY": "usd",
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Location.String() != "Europe/Dublin" {
		t.Errorf("Location = %q, want Europe/Dublin from DOMUS_TZ", cfg.Location)
	}
	if cfg.Currency != "USD" {
		t.Errorf("Currency = %q, want USD: INGLE_CURRENCY beats DOMUS_CURRENCY", cfg.Currency)
	}
	if fmt.Sprint(cfg.Legacy) != "[DOMUS_TZ]" {
		t.Errorf("Legacy = %v, want [DOMUS_TZ]", cfg.Legacy)
	}
}

// A data folder from before the rename keeps using its domus.db.
func TestLoadLegacyDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		exists []string
		want   string
	}{
		{"new install", nil, nil, "data/ingle.db"},
		{"old install", nil, []string{"data/domus.db"}, "data/domus.db"},
		{"both: ingle.db wins", nil, []string{"data/domus.db", "data/ingle.db"}, "data/ingle.db"},
		{"docker, old volume", map[string]string{"INGLE_DB_PATH": "/data/ingle.db"}, []string{"/data/domus.db"}, "/data/domus.db"},
		{"a path of your own is left alone", map[string]string{"INGLE_DB_PATH": "/srv/house.db"}, []string{"/srv/domus.db"}, "/srv/house.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := fileExists
			t.Cleanup(func() { fileExists = old })
			fileExists = func(p string) bool {
				for _, e := range tc.exists {
					if filepath.Clean(p) == filepath.Clean(e) {
						return true
					}
				}
				return false
			}
			cfg, err := load(func(k string) string { return tc.env[k] })
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if filepath.Clean(cfg.DBPath) != filepath.Clean(tc.want) {
				t.Errorf("DBPath = %q, want %q", cfg.DBPath, tc.want)
			}
		})
	}
}
