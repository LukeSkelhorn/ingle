// Command domus runs the Domus family chores board server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/avatar"
	"github.com/lukeskelhorn/domus/internal/backup"
	"github.com/lukeskelhorn/domus/internal/calendar"
	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/config"
	householdpkg "github.com/lukeskelhorn/domus/internal/household"
	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/internal/lock"
	"github.com/lukeskelhorn/domus/internal/photos"
	"github.com/lukeskelhorn/domus/internal/server"
	"github.com/lukeskelhorn/domus/internal/settings"
	"github.com/lukeskelhorn/domus/internal/store"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		err = run(log)
	case "healthcheck":
		// For Docker's HEALTHCHECK: the image has no shell or curl.
		err = healthcheck()
	case "backup":
		err = backupNow()
	case "version", "--version", "-v":
		fmt.Println("domus", version)
	default:
		fmt.Fprintf(os.Stderr, "usage: domus [healthcheck|backup|version]\n")
		os.Exit(2)
	}
	if err != nil {
		log.Error("domus "+cmd+" failed", slog.Any("err", err))
		os.Exit(1)
	}
}

// healthcheck asks the running server on DOMUS_ADDR for /healthz.
func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return fmt.Errorf("DOMUS_ADDR %q: %w", cfg.Addr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

// backupNow writes one backup and prints its path, e.g. before an upgrade:
// docker compose exec domus /domus backup.
func backupNow() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	path, err := backup.Run(ctx, db, filepath.Dir(cfg.DBPath), cfg.BackupDir, cfg.BackupKeep, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
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
	// DOMUS_TZ seeds the timezone for a new household; after that it is
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
	go screensaverLoop(ctx, log, library, cals)
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

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpSrv.RegisterOnShutdown(srv.CloseStreams)

	errCh := make(chan error, 1)
	go func() {
		log.Info("domus listening", slog.String("version", version), slog.String("addr", cfg.Addr), slog.String("db", cfg.DBPath), slog.String("tz", cfg.Location.String()),
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

// screensaverLoop keeps the screensaver's sources fresh: calendar feeds are
// re-fetched every calendar.RefreshEvery and the photos folder is
// re-indexed every 10 minutes. Both run once at startup.
func screensaverLoop(ctx context.Context, log *slog.Logger, library *photos.Library, cals *calendar.Service) {
	rescan := func() {
		if err := library.Rescan(ctx); err != nil && ctx.Err() == nil {
			log.Error("scan photos folder", slog.Any("err", err))
		}
	}
	refresh := func() {
		if err := cals.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Error("refresh calendars", slog.Any("err", err))
		}
	}
	rescan()
	refresh()
	photoTick := time.NewTicker(10 * time.Minute)
	defer photoTick.Stop()
	calTick := time.NewTicker(calendar.RefreshEvery)
	defer calTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-photoTick.C:
			rescan()
		case <-calTick.C:
			refresh()
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
