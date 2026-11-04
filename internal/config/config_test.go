package config

import (
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
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"DOMUS_ADDR":       "127.0.0.1:9000",
		"DOMUS_TZ":         "Europe/Dublin",
		"DOMUS_WEEK_START": "Sunday",
		"DOMUS_CURRENCY":   "eur",
		"DOMUS_PHOTOS_DIR": "/photos",
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
		{"DOMUS_TZ": "Not/AZone"},
		{"DOMUS_WEEK_START": "funday"},
	} {
		if _, err := load(func(k string) string { return tc[k] }); err == nil {
			t.Errorf("load(%v) succeeded, want error", tc)
		}
	}
}
