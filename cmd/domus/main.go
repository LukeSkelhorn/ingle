// Command domus runs the Domus family chores board server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/avatar"
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

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("domus exited", slog.Any("err", err))
		os.Exit(1)
	}
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

	go maintenanceLoop(ctx, log, set, choreSvc, authSvc, household.ID)
	go screensaverLoop(ctx, log, library, cals)
	lockSvc := lock.New(st, set, authSvc.EndDisplaySessions)
	go lockLoop(ctx, log, lockSvc, household.ID)

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
		Log: log,
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
		log.Info("domus listening", slog.String("addr", cfg.Addr), slog.String("db", cfg.DBPath), slog.String("tz", cfg.Location.String()),
			slog.String("photos_dir", cfg.PhotosDir))
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
