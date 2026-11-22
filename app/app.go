// Package app runs an Ingle server and is the core's public entry point.
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
	"slices"
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
	"github.com/lukeskelhorn/ingle/internal/secrets"
	"github.com/lukeskelhorn/ingle/internal/server"
	"github.com/lukeskelhorn/ingle/internal/settings"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/takeout"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Options are what a program passes to [Run].
type Options struct {
	Version string
	Log     *slog.Logger

	ResolveHousehold func(r *http.Request) (id int64, ok bool, err error)
	Routes           func(mux *http.ServeMux, h Helpers)
	AdminLinks       []AdminLink
	PhotoBlobs       PhotoBlobs
	Jobs             JobMode
	SignIn           *SignIn
	// HouseholdState is asked on every change and admin page, so keep it quick.
	HouseholdState func(ctx context.Context, hh int64) (HouseholdState, error)
	// HouseholdDeleting runs before household hh is deleted from Admin.
	// An error stops the delete, and the admin is told nothing was removed.
	HouseholdDeleting func(ctx context.Context, hh int64) error
	// HouseholdDeleted runs after household hh has been deleted.
	HouseholdDeleted func(ctx context.Context, hh int64)
	// Restrict, if set, is asked before the signed-in adult does action to
	// target, one of the Action constants. A non-empty reason refuses it,
	// and is shown to them instead of the button.
	Restrict func(r *http.Request, action string, target int64) (reason string)
	// DisplayActions are links under the household displays in Admin › Devices.
	DisplayActions []AdminLink
	// HeadScripts are extra script paths every core page loads, deferred,
	// for example ones served by Routes.
	HeadScripts []string
}

// HouseholdState is how a household is, as the program running Ingle says.
type HouseholdState struct {
	// ReadOnly refuses changes with Notice; signing in and out, locking and deleting the household still work.
	ReadOnly bool
	// Notice is shown on admin pages, and on the board when ReadOnly.
	Notice     string
	NoticeLink AdminLink
}

// Actions an adult can be refused through [Options.Restrict].
const (
	// ActionDeleteHousehold is deleting the household; target is its id.
	ActionDeleteHousehold = server.ActionDeleteHousehold
	// ActionArchivePerson is archiving someone; target is their person id.
	ActionArchivePerson = server.ActionArchivePerson
)

// SignIn is another service that signs adults in instead of Ingle's own sign-in.
type SignIn struct {
	URL         string
	OnSignOut   func(r *http.Request) string
	SessionIdle time.Duration
}

// AdminLink is a link in the Admin bar.
type AdminLink struct {
	Label, Path string
}

// PhotoBlobs stores the files of uploaded photos by name.
type PhotoBlobs = photos.Blobs

// JobMode is where the background jobs run.
type JobMode int

// Job modes.
const (
	JobsHere JobMode = iota
	JobsElsewhere
)

// Helpers are for handlers added through Options.Routes.
type Helpers struct{ srv *server.Server }

// RequireAdmin lets only a signed-in adult of the request's household reach h.
func (h Helpers) RequireAdmin(next http.Handler) http.Handler { return h.srv.RequireAdmin(next) }

// RequireViewer lets only a household display or a signed-in adult reach h.
func (h Helpers) RequireViewer(next http.Handler) http.Handler { return h.srv.RequireViewer(next) }

// StartSession starts an Ingle session for an adult the SignIn service has confirmed.
func (h Helpers) StartSession(w http.ResponseWriter, r *http.Request, hh, personID int64) error {
	return h.srv.StartSession(w, r, hh, personID)
}

// RegisterDisplay makes the browser of r a household display of hh, named name, added by adult by.
// It ends any session on that browser.
func (h Helpers) RegisterDisplay(w http.ResponseWriter, r *http.Request, hh int64, name string, by int64) error {
	return h.srv.RegisterDisplay(w, r, hh, name, by)
}

// CreateHouseholdInput is a new household and its first adult.
type CreateHouseholdInput struct {
	Name     string
	Timezone string
	Adult    string
	Colour   string
	Emoji    string
	Email    string
}

// FieldErrors are a message for each invalid field.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return valid.Errors(e).Error() }

func fieldErrors(err error) error {
	var ve valid.Errors
	if errors.As(err, &ve) {
		return FieldErrors(ve)
	}
	return err
}

// CreateHousehold makes a household and its first adult; invalid input is a FieldErrors.
func (h Helpers) CreateHousehold(ctx context.Context, in CreateHouseholdInput) (householdID, personID int64, err error) {
	hh, p, err := h.srv.CreateHousehold(ctx, householdpkg.NewHouseholdInput{
		HouseholdName: in.Name,
		Timezone:      in.Timezone,
		Person:        householdpkg.PersonInput{Name: in.Adult, Colour: in.Colour},
		Emoji:         in.Emoji,
		Email:         in.Email,
	})
	return hh, p, fieldErrors(err)
}

// SetPIN sets the PIN of the adult signed in on r; an invalid PIN is a FieldErrors.
func (h Helpers) SetPIN(r *http.Request, pin string) error {
	return fieldErrors(h.srv.SetPIN(r, pin))
}

// AdultOf returns the adult signed in on a request, if any.
func AdultOf(r *http.Request) (personID int64, ok bool) { return server.AdultOf(r) }

// PersonColours are the colours offered for people.
func PersonColours() []string { return slices.Clone(householdpkg.Colours) }

// PersonEmojis are the emoji offered as people's pictures.
func PersonEmojis() []string { return slices.Clone(avatar.Emojis) }

// HouseholdOf returns the household a request is for, or 0 if not yet known.
func HouseholdOf(r *http.Request) int64 { return server.HouseholdOf(r) }

// HouseholdFromContext returns the household of the request a context
// came from, or 0 if not yet known. Log records carry that context.
func HouseholdFromContext(ctx context.Context) int64 { return store.HouseholdFrom(ctx) }

// Run serves on INGLE_ADDR until ctx is cancelled, then shuts down gracefully.
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

// RunJobs runs only the background jobs until ctx is cancelled.
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
	backupLoc  func() *time.Location
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

	db, err := openDB(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c.db = db
	if bypass, err := store.CanBypassRLS(ctx, db); err != nil {
		return nil, err
	} else if bypass {
		log.Warn("the database role can bypass row-level security, so households aren't kept apart by the database: connect as a role that isn't a superuser and hasn't BYPASSRLS")
	}
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
		ctx := store.WithHousehold(ctx, household.ID)
		if err := c.set.Ensure(ctx, household.ID, cfg.Location); err != nil {
			return nil, err
		}
		single = household.ID
		c.households = func(context.Context) ([]int64, error) { return []int64{single}, nil }
		c.backupLoc = func() *time.Location { return c.set.Location(ctx, single) }
	} else {
		c.households = func(ctx context.Context) ([]int64, error) {
			return st.Q.ListHouseholdIDs(store.AsSystem(ctx))
		}
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
	keys, err := secrets.New(st, cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	cals := calendar.New(st, c.set.Location, nil, log)
	cals.SetSecrets(keys)
	c.auth = auth.New(st, c.set)
	hhSvc := householdpkg.New(st)
	var signIn *server.SignIn
	if opts.SignIn != nil {
		if opts.SignIn.URL == "" {
			return nil, errors.New("app: SignIn.URL is empty")
		}
		signIn = &server.SignIn{URL: opts.SignIn.URL, OnSignOut: opts.SignIn.OnSignOut}
		c.auth.SetPersonalIdle(opts.SignIn.SessionIdle)
		hhSvc.SetExternalSignIn(true)
	}
	c.chores = chores.New(st, c.set.Location)
	c.chores.SetDayStart(c.set.DayStart)
	c.lock = lock.New(st, c.set, c.auth.EndDisplaySessions)

	adminLinks := make([]module.NavItem, 0, len(opts.AdminLinks))
	for _, l := range opts.AdminLinks {
		adminLinks = append(adminLinks, module.NavItem{Key: l.Path, Label: l.Label, Path: l.Path})
	}
	displayActions := make([]module.NavItem, 0, len(opts.DisplayActions))
	for _, l := range opts.DisplayActions {
		displayActions = append(displayActions, module.NavItem{Key: l.Path, Label: l.Label, Path: l.Path})
	}
	var routes func(*server.Server, *http.ServeMux)
	if opts.Routes != nil {
		routes = func(s *server.Server, mux *http.ServeMux) { opts.Routes(mux, Helpers{srv: s}) }
	}

	var state func(context.Context, int64) (server.HouseholdState, error)
	if opts.HouseholdState != nil {
		state = func(ctx context.Context, hh int64) (server.HouseholdState, error) {
			st, err := opts.HouseholdState(ctx, hh)
			return server.HouseholdState{
				ReadOnly:    st.ReadOnly,
				Notice:      st.Notice,
				NoticeLabel: st.NoticeLink.Label,
				NoticePath:  st.NoticeLink.Path,
			}, err
		}
	}

	c.srv, err = server.New(server.Deps{
		State:             state,
		HouseholdDeleting: opts.HouseholdDeleting,
		HouseholdDeleted:  opts.HouseholdDeleted,
		Restrict:          opts.Restrict,
		DisplayActions:    displayActions,
		HeadScripts:       opts.HeadScripts,
		Store:             st,
		HouseholdID:       single,
		ResolveHousehold:  opts.ResolveHousehold,
		Households:        c.households,
		Routes:            routes,
		AdminLinks:        adminLinks,
		Auth:              c.auth,
		Settings:          c.set,
		Chores:            c.chores,
		Household:         hhSvc,
		Avatars:           avatars,
		Photos:            library,
		Calendars:         cals,
		Lock:              c.lock,
		Takeout:           takeout.New(st, keys, library, avatars, log),
		Modules:           module.NewService(st, module.NewRegistry()),
		Locale: locale.Locale{
			Location:   cfg.Location,
			DateFormat: cfg.DateFormat,
			WeekStart:  cfg.WeekStart,
			Currency:   cfg.Currency,
		},
		Log:            log,
		TrustedProxies: cfg.TrustedProxies,
		Version:        c.version,
		SignIn:         signIn,
		// On SQLite the clear-out follows the nightly backup, so none without backups.
		NoClearOut: cfg.BackupKeep == 0 && store.EngineOf(db) == store.SQLite,
	})
	if err != nil {
		return nil, err
	}
	ok = true
	return c, nil
}

func (c *core) runJobs(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { maintenanceLoop(ctx, c.log, c.set, c.chores, c.auth, c.households) })
	wg.Go(func() { lockLoop(ctx, c.log, c.lock, c.households) })
	if c.cfg.BackupKeep > 0 {
		if err := backup.Check(c.cfg.BackupDir); err != nil {
			c.log.Warn("backup dir not writable; nightly backups will fail", slog.String("dir", c.cfg.BackupDir), slog.Any("err", err))
		}
		wg.Go(func() { backupLoop(ctx, c.log, c.db, filepath.Dir(c.cfg.DBPath), c.cfg, c.backupLoc, c.clearHistory) })
	} else if store.EngineOf(c.db) == store.Postgres {
		wg.Go(func() { nightlyLoop(ctx, c.cfg.BackupAt, c.backupLoc, c.clearHistory) })
	}
	wg.Go(func() { c.srv.RunModuleLoops(ctx) })
	wg.Wait()
}

func (c *core) clearHistory(ctx context.Context) {
	cleared := false
	eachHousehold(ctx, c.log, c.households, func(ctx context.Context, householdID int64) {
		log := c.log.With(slog.Int64("household", householdID))
		n, err := c.chores.ClearHistory(ctx, householdID)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("history.clear_failed", slog.Any("err", err))
			}
			return
		}
		if n.Any() {
			cleared = true
			log.Info("history.cleared",
				slog.Int64("ledger_folded", n.Folded), slog.Int64("completions", n.Completions),
				slog.Int64("occurrences", n.Occurrences), slog.Int64("claims", n.Redemptions),
				slog.Int64("bank_requests", n.Transfers), slog.Int64("activity", n.Activity))
		}
	})
	if cleared && store.EngineOf(c.db) == store.SQLite {
		if _, err := c.db.ExecContext(ctx, "VACUUM"); err != nil && ctx.Err() == nil {
			c.log.Error("history.vacuum_failed", slog.Any("err", err))
		}
	}
}

func eachHousehold(ctx context.Context, log *slog.Logger, households func(context.Context) ([]int64, error), fn func(ctx context.Context, householdID int64)) {
	ids, err := households(store.AsSystem(ctx))
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
		fn(store.WithHousehold(ctx, id), id)
	}
}

// Healthcheck asks the server running on INGLE_ADDR for /healthz.
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

func openDB(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	if cfg.DatabaseURL != "" {
		return store.OpenPostgres(ctx, store.PostgresOptions{URL: cfg.DatabaseURL, MigrateURL: cfg.DatabaseMigrateURL})
	}
	return store.Open(ctx, cfg.DBPath)
}

// Backup writes one backup of the database and returns its path.
func Backup(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	db, err := openDB(ctx, cfg)
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

func maintenanceLoop(ctx context.Context, log *slog.Logger, set *settings.Service, svc *chores.Service, authSvc *auth.Service, households func(context.Context) ([]int64, error)) {
	one := func(ctx context.Context, householdID int64) {
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
		if n, err := svc.PayInterest(ctx, householdID); err != nil {
			if ctx.Err() == nil {
				log.Error("pay interest", slog.Any("err", err))
			}
		} else if n > 0 {
			log.Info("paid interest", slog.Int("payments", n))
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

func lockLoop(ctx context.Context, log *slog.Logger, svc *lock.Service, households func(context.Context) ([]int64, error)) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		eachHousehold(ctx, log, households, func(ctx context.Context, householdID int64) {
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

func nightlyLoop(ctx context.Context, clock string, loc func() *time.Location, job func(context.Context)) {
	for {
		// Re-read each night: the timezone can change in Admin › Settings.
		next := backup.Next(time.Now(), loc(), clock)
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		job(ctx)
	}
}

func backupLoop(ctx context.Context, log *slog.Logger, db *sql.DB, dataDir string, cfg config.Config, loc func() *time.Location, after func(context.Context)) {
	nightlyLoop(ctx, cfg.BackupAt, loc, func(ctx context.Context) {
		start := time.Now()
		path, err := backup.Run(ctx, db, dataDir, cfg.BackupDir, cfg.BackupKeep, start.In(loc()))
		if err != nil {
			if ctx.Err() == nil {
				log.Error("backup.failed", slog.Any("err", err))
			}
			return
		}
		var size int64
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
		log.Info("backup.done", slog.String("path", path), slog.Int64("bytes", size), slog.Duration("took", time.Since(start)))
		after(ctx)
	})
}
