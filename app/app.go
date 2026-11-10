// Package app runs an Ingle server. It is the core's public entry point:
// cmd/ingle is a thin wrapper around it, and other programs (such as Ingle
// Hosted) import the core as a Go module and start it the same way.
//
// Everything else in the core stays under internal/. Settings come from the
// INGLE_* environment variables, as for cmd/ingle.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/backup"
	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/config"
	householdpkg "github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/lock"
	"github.com/lukeskelhorn/ingle/internal/module"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/server"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
)

// Options are what a program passes to [Run].
type Options struct {
	// Version is the build's version, shown in the UI and logs; empty
	// means "dev".
	Version string
	// Log is where the server logs; nil means slog.Default().
	Log *slog.Logger
}

// Run starts the server on INGLE_ADDR and serves until ctx is cancelled, then
// shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Legacy) > 0 {
		log.Warn("Ingle used to be called Domus: these settings still work, but rename them to INGLE_*",
			slog.Any("settings", cfg.Legacy))
	}

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	st := store.New(db)
	household, err := st.EnsureHousehold(ctx, "Home", time.Now())
	if err != nil {
		return err
	}
	set := settings.New(st)
	// INGLE_TZ seeds the timezone for a new household; after that it is
	// changed under Admin › Settings.
	if err := set.Ensure(ctx, household.ID, cfg.Location); err != nil {
		return err
	}
	dataDir := filepath.Dir(cfg.DBPath)
	avatars, err := avatar.NewStore(filepath.Join(dataDir, "avatars"))
	if err != nil {
		return err
	}
	library, err := photos.New(st, dataDir, cfg.PhotosDir, log)
	if err != nil {
		return err
	}
	cals := calendar.New(st, set.Location, nil, log)
	authSvc := auth.New(st, set)
	choreSvc := chores.New(st, set.Location)
	choreSvc.SetDayStart(set.DayStart)

	go maintenanceLoop(ctx, log, set, choreSvc, authSvc, household.ID)
	lockSvc := lock.New(st, set, authSvc.EndDisplaySessions)
	go lockLoop(ctx, log, lockSvc, household.ID)
	if cfg.BackupKeep > 0 {
		if err := backup.Check(cfg.BackupDir); err != nil {
			log.Warn("backup dir not writable; nightly backups will fail", slog.String("dir", cfg.BackupDir), slog.Any("err", err))
		}
		go backupLoop(ctx, log, db, dataDir, cfg, func() *time.Location { return set.Location(ctx, household.ID) })
	}

	srv, err := server.New(server.Deps{
		Store:       st,
		HouseholdID: household.ID,
		Auth:        authSvc,
		Settings:    set,
		Chores:      choreSvc,
		Household:   householdpkg.New(st),
		Avatars:     avatars,
		Photos:      library,
		Calendars:   cals,
		Lock:        lockSvc,
		// Ingle's modules are registered in a fixed order by server.New.
		Modules: module.NewService(st, module.NewRegistry()),
		Locale: locale.Locale{
			Location:   cfg.Location,
			DateFormat: cfg.DateFormat,
			WeekStart:  cfg.WeekStart,
			Currency:   cfg.Currency,
		},
		Log:            log,
		TrustedProxies: cfg.TrustedProxies,
		Version:        version,
	})
	if err != nil {
		return err
	}
	// The modules' background jobs: calendar feeds and the photos folder.
	go srv.RunModuleLoops(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpSrv.RegisterOnShutdown(srv.CloseStreams)

	errCh := make(chan error, 1)
	go func() {
		log.Info("ingle listening", slog.String("version", version), slog.String("addr", cfg.Addr), slog.String("db", cfg.DBPath), slog.String("tz", cfg.Location.String()),
			slog.String("photos_dir", cfg.PhotosDir), slog.String("backup_dir", cfg.BackupDir), slog.Int("backup_keep", cfg.BackupKeep),
			slog.Int("trusted_proxies", len(cfg.TrustedProxies)))
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// Healthcheck asks the server running on INGLE_ADDR for /healthz, e.g. for
// Docker's HEALTHCHECK.
func Healthcheck(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return fmt.Errorf("INGLE_ADDR %q: %w", cfg.Addr, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", port)+"/healthz", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

// Backup writes one backup of the database and returns its path, e.g.
// before an upgrade.
func Backup(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	path, err := backup.Run(ctx, db, filepath.Dir(cfg.DBPath), cfg.BackupDir, cfg.BackupKeep, time.Now())
	if err != nil {
		return "", err
	}
	return path, nil
}

// maintenanceLoop runs once at startup and then hourly: it keeps the rolling
// window of chore occurrences filled (so new days and newly added chores
// appear) from the household's current board day, and sweeps expired
// sessions.
func maintenanceLoop(ctx context.Context, log *slog.Logger, set *settings.Service, svc *chores.Service, authSvc *auth.Service, householdID int64) {
	run := func() {
		s, err := set.Get(ctx, householdID)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("load settings", slog.Any("err", err))
			}
			return
		}
		n, err := svc.Materialise(ctx, householdID, s.BoardDay(time.Now()), chores.Window)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("materialise chores", slog.Any("err", err))
			}
		} else if n > 0 {
			log.Info("materialised chore occurrences", slog.Int("created", n))
		}
		if _, err := authSvc.Sweep(ctx, householdID); err != nil && ctx.Err() == nil {
			log.Error("sweep sessions", slog.Any("err", err))
		}
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// lockLoop applies the displays' lock schedule and idle lock every minute.
func lockLoop(ctx context.Context, log *slog.Logger, svc *lock.Service, householdID int64) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := svc.Tick(ctx, householdID); err != nil && ctx.Err() == nil {
			log.Error("lock tick", slog.Any("err", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// backupLoop writes a backup every day at cfg.BackupAt household time,
// keeping the newest cfg.BackupKeep.
func backupLoop(ctx context.Context, log *slog.Logger, db *sql.DB, dataDir string, cfg config.Config, loc func() *time.Location) {
	for {
		// Re-read the timezone each night: it can change in Admin › Settings.
		next := backup.Next(time.Now(), loc(), cfg.BackupAt)
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		start := time.Now()
		path, err := backup.Run(ctx, db, dataDir, cfg.BackupDir, cfg.BackupKeep, start.In(loc()))
		if err != nil {
			if ctx.Err() == nil {
				log.Error("backup.failed", slog.Any("err", err))
			}
			continue
		}
		var size int64
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
		log.Info("backup.done", slog.String("path", path), slog.Int64("bytes", size), slog.Duration("took", time.Since(start)))
	}
}
