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
	"sync"
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

	// ResolveHousehold names the household a request is for, before its
	// display or session is known (e.g. from the address it came to). ok
	// is false when it can't tell; the request's display or session then
	// decides. Setting it serves many households: no household is made at
	// startup, and the background jobs run for every household in the
	// database. Nil serves the single household of a self-hosted server.
	ResolveHousehold func(r *http.Request) (id int64, ok bool, err error)
	// Routes adds a program's own routes, such as sign-up or billing.
	// They sit behind the same cross-origin check and identity as Ingle's
	// own; use [Helpers] to require a signed-in adult.
	Routes func(mux *http.ServeMux, h Helpers)
	// AdminLinks are extra links in the Admin bar, after the modules'.
	AdminLinks []AdminLink
	// PhotoBlobs stores uploaded photos' files; nil keeps them on disk
	// beside the database.
	PhotoBlobs PhotoBlobs
	// Jobs is where the background jobs run: here, beside the web server
	// (the default), or elsewhere, in a process that calls [RunJobs].
	Jobs JobMode
}

// AdminLink is a link in the Admin bar.
type AdminLink struct {
	// Label is the link text and Path where it goes, e.g. "Billing" and
	// "/admin/billing". Path also marks the link current on its own page.
	Label, Path string
}

// PhotoBlobs stores the files of uploaded photos by name. Its methods use
// only standard library types, so another module can implement it.
type PhotoBlobs = photos.Blobs

// JobMode is where the background jobs run.
type JobMode int

const (
	// JobsHere runs the background jobs in the same process as the web
	// server.
	JobsHere JobMode = iota
	// JobsElsewhere leaves them to another process running [RunJobs].
	JobsElsewhere
)

// Helpers are for handlers added through Options.Routes.
type Helpers struct{ srv *server.Server }

// RequireAdmin lets only a signed-in adult of the request's household
// reach h; anyone else is sent to sign in.
func (h Helpers) RequireAdmin(next http.Handler) http.Handler { return h.srv.RequireAdmin(next) }

// RequireViewer lets only a household display or a signed-in adult reach
// h.
func (h Helpers) RequireViewer(next http.Handler) http.Handler { return h.srv.RequireViewer(next) }

// HouseholdOf returns the household a request is for, or 0 when none is
// known yet (nobody signed in and the resolver couldn't tell).
func HouseholdOf(r *http.Request) int64 { return server.HouseholdOf(r) }

// Run starts the server on INGLE_ADDR and serves until ctx is cancelled, then
// shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	c, err := build(ctx, opts)
	if err != nil {
		return err
	}
	defer c.db.Close()
	if opts.Jobs == JobsHere {
		go c.runJobs(ctx)
	}

	httpSrv := &http.Server{
		Addr:              c.cfg.Addr,
		Handler:           c.srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpSrv.RegisterOnShutdown(c.srv.CloseStreams)

	cfg, log := c.cfg, c.log
	errCh := make(chan error, 1)
	go func() {
		log.Info("ingle listening", slog.String("version", c.version), slog.String("addr", cfg.Addr), slog.String("db", cfg.DBLabel()), slog.String("tz", cfg.Location.String()),
			slog.String("photos_dir", cfg.PhotosDir), slog.String("backup_dir", cfg.BackupDir), slog.Int("backup_keep", cfg.BackupKeep),
			slog.Int("trusted_proxies", len(cfg.TrustedProxies)), slog.Bool("many_households", opts.ResolveHousehold != nil),
			slog.Bool("jobs", opts.Jobs == JobsHere))
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

// RunJobs runs only the background jobs (chores, the lock schedule,
// backups and the modules' jobs) until ctx is cancelled, for a worker
// beside web servers started with JobsElsewhere. Its Options should match
// theirs.
func RunJobs(ctx context.Context, opts Options) error {
	c, err := build(ctx, opts)
	if err != nil {
		return err
	}
	defer c.db.Close()
	c.log.Info("ingle jobs running", slog.String("version", c.version), slog.String("db", c.cfg.DBLabel()))
	c.runJobs(ctx)
	return nil
}

// core is a built server and its services, not yet serving.
type core struct {
	cfg        config.Config
	log        *slog.Logger
	version    string
	db         *sql.DB
	set        *settings.Service
	auth       *auth.Service
	chores     *chores.Service
	lock       *lock.Service
	srv        *server.Server
	households func(context.Context) ([]int64, error)
	// backupLoc is the timezone nightly backups follow.
	backupLoc func() *time.Location
}

func build(ctx context.Context, opts Options) (*core, error) {
	c := &core{log: opts.Log, version: opts.Version}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.version == "" {
		c.version = "dev"
	}
	log := c.log

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	c.cfg = cfg
	if len(cfg.Legacy) > 0 {
		log.Warn("Ingle used to be called Domus: these settings still work, but rename them to INGLE_*",
			slog.Any("settings", cfg.Legacy))
	}

	db, err := store.Open(ctx, cfg.DSN())
	if err != nil {
		return nil, err
	}
	c.db = db
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()

	st := store.New(db)
	c.set = settings.New(st)
	var single int64
	if opts.ResolveHousehold == nil {
		household, err := st.EnsureHousehold(ctx, "Home", time.Now())
		if err != nil {
			return nil, err
		}
		// INGLE_TZ seeds the timezone for a new household; after that it
		// is changed under Admin › Settings.
		if err := c.set.Ensure(ctx, household.ID, cfg.Location); err != nil {
			return nil, err
		}
		single = household.ID
		c.households = func(context.Context) ([]int64, error) { return []int64{single}, nil }
		c.backupLoc = func() *time.Location { return c.set.Location(ctx, single) }
	} else {
		c.households = st.Q.ListHouseholdIDs
		c.backupLoc = func() *time.Location { return cfg.Location }
	}

	dataDir := filepath.Dir(cfg.DBPath)
	avatars, err := avatar.NewStore(filepath.Join(dataDir, "avatars"))
	if err != nil {
		return nil, err
	}
	library, err := photos.New(st, dataDir, cfg.PhotosDir, log)
	if err != nil {
		return nil, err
	}
	if opts.PhotoBlobs != nil {
		library.SetBlobs(opts.PhotoBlobs)
	}
	cals := calendar.New(st, c.set.Location, nil, log)
	c.auth = auth.New(st, c.set)
	c.chores = chores.New(st, c.set.Location)
	c.chores.SetDayStart(c.set.DayStart)
	c.lock = lock.New(st, c.set, c.auth.EndDisplaySessions)

	adminLinks := make([]module.NavItem, 0, len(opts.AdminLinks))
	for _, l := range opts.AdminLinks {
		adminLinks = append(adminLinks, module.NavItem{Key: l.Path, Label: l.Label, Path: l.Path})
	}
	var routes func(*server.Server, *http.ServeMux)
	if opts.Routes != nil {
		routes = func(s *server.Server, mux *http.ServeMux) { opts.Routes(mux, Helpers{srv: s}) }
	}

	c.srv, err = server.New(server.Deps{
		Store:            st,
		HouseholdID:      single,
		ResolveHousehold: opts.ResolveHousehold,
		Households:       c.households,
		Routes:           routes,
		AdminLinks:       adminLinks,
		Auth:             c.auth,
		Settings:         c.set,
		Chores:           c.chores,
		Household:        householdpkg.New(st),
		Avatars:          avatars,
		Photos:           library,
		Calendars:        cals,
		Lock:             c.lock,
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
		Version:        c.version,
	})
	if err != nil {
		return nil, err
	}
	ok = true
	return c, nil
}

// runJobs runs the background jobs until ctx ends.
func (c *core) runJobs(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { maintenanceLoop(ctx, c.log, c.set, c.chores, c.auth, c.households) })
	wg.Go(func() { lockLoop(ctx, c.log, c.lock, c.households) })
	if c.cfg.BackupKeep > 0 {
		if err := backup.Check(c.cfg.BackupDir); err != nil {
			c.log.Warn("backup dir not writable; nightly backups will fail", slog.String("dir", c.cfg.BackupDir), slog.Any("err", err))
		}
		wg.Go(func() { backupLoop(ctx, c.log, c.db, filepath.Dir(c.cfg.DBPath), c.cfg, c.backupLoc) })
	}
	// The modules' background jobs: calendar feeds and the photos folder.
	wg.Go(func() { c.srv.RunModuleLoops(ctx) })
	wg.Wait()
}

// eachHousehold runs fn for every household, logging a failure to list
// them.
func eachHousehold(ctx context.Context, log *slog.Logger, households func(context.Context) ([]int64, error), fn func(householdID int64)) {
	ids, err := households(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("list households", slog.Any("err", err))
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		fn(id)
	}
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
	db, err := store.Open(ctx, cfg.DSN())
	if err != nil {
		return "", err
	}
	defer db.Close()
	if store.EngineOf(db) != store.SQLite {
		return "", errors.New("backups are for SQLite; use the Postgres service's own backups")
	}
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
func maintenanceLoop(ctx context.Context, log *slog.Logger, set *settings.Service, svc *chores.Service, authSvc *auth.Service, households func(context.Context) ([]int64, error)) {
	one := func(householdID int64) {
		log := log.With(slog.Int64("household", householdID))
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
	run := func() { eachHousehold(ctx, log, households, one) }
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
func lockLoop(ctx context.Context, log *slog.Logger, svc *lock.Service, households func(context.Context) ([]int64, error)) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		eachHousehold(ctx, log, households, func(householdID int64) {
			if err := svc.Tick(ctx, householdID); err != nil && ctx.Err() == nil {
				log.Error("lock tick", slog.Int64("household", householdID), slog.Any("err", err))
			}
		})
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
