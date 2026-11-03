// Command domus runs the Domus family chores board server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/config"
	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/internal/server"
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
	go materialiseLoop(ctx, log, chores.New(st, cfg.Location), household.ID)

	srv, err := server.New(db, locale.Locale{
		Location:   cfg.Location,
		DateFormat: cfg.DateFormat,
		WeekStart:  cfg.WeekStart,
		Currency:   cfg.Currency,
	}, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("domus listening", slog.String("addr", cfg.Addr), slog.String("db", cfg.DBPath), slog.String("tz", cfg.Location.String()))
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

// materialiseWindow is how many days ahead chore occurrences are created.
const materialiseWindow = 14

// materialiseLoop keeps the rolling window of chore occurrences filled: once
// at startup, then hourly so new days (and newly added chores) appear.
func materialiseLoop(ctx context.Context, log *slog.Logger, svc *chores.Service, householdID int64) {
	run := func() {
		n, err := svc.Materialise(ctx, householdID, time.Now(), materialiseWindow)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("materialise chores", slog.Any("err", err))
			}
			return
		}
		if n > 0 {
			log.Info("materialised chore occurrences", slog.Int("created", n))
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
