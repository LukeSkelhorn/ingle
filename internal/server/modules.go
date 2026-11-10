package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/module"
)

// Module IDs. They are stored in the database, so never change one.
const (
	modCalendar = "calendar"
	modPhotos   = "photos"
	modRewards  = "rewards"
	modMoney    = "money"
)

// photoRescanEvery is how often the photos folder is re-indexed.
const photoRescanEvery = 10 * time.Minute

// registerModules adds Ingle's modules. The order is the order in Admin ›
// Modules and in the admin bar, and a module's requirements come first.
func (s *Server) registerModules(reg *module.Registry) {
	reg.Register(rewardsModule{s})
	reg.Register(moneyModule{s})
	reg.Register(photosModule{s})
	reg.Register(calendarModule{s})
}

// moduleOn reports whether the module id is on for the household.
func (s *Server) moduleOn(ctx context.Context, id string) bool {
	return s.Modules.Enabled(ctx, householdIn(ctx), id)
}

// moduleNav is the admin bar links of the modules that are on.
func (s *Server) moduleNav(ctx context.Context) []module.NavItem {
	var nav []module.NavItem
	for _, m := range s.Modules.Registry().All() {
		if s.moduleOn(ctx, m.ID()) {
			nav = append(nav, m.AdminNav()...)
		}
	}
	return append(nav, s.AdminLinks...)
}

// requireModule answers 404 while the module id is off.
func (s *Server) requireModule(id string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.moduleOn(r.Context(), id) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// moduleRouter mounts a module's routes behind the usual guards and its
// on/off switch.
type moduleRouter struct {
	s   *Server
	mux *http.ServeMux
	id  string
}

func (r moduleRouter) Viewer(pattern string, h http.HandlerFunc) {
	r.mux.Handle(pattern, r.s.requireViewer(r.s.requireModule(r.id, r.s.lockGate(h))))
}

func (r moduleRouter) Admin(pattern string, h http.HandlerFunc) {
	r.mux.Handle(pattern, r.s.requireAdmin(r.s.requireModule(r.id, r.s.publishAfterWrite(h))))
}

// RunModuleLoops runs every module's background loops until ctx ends. Each
// loop runs at startup and then on its interval, and skips the household
// while its module is off. Switching a module on runs its loops straight
// away, so its data is fresh.
func (s *Server) RunModuleLoops(ctx context.Context) {
	var wg sync.WaitGroup
	for _, m := range s.Modules.Registry().All() {
		for _, l := range m.Loops() {
			kick := make(chan struct{}, 1)
			s.loopKicks = append(s.loopKicks, kick)
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runLoop(ctx, m.ID(), l, kick)
			}()
		}
	}
	s.loopsStarted.Store(true)
	wg.Wait()
}

// kickLoops tells the module loops the modules changed.
func (s *Server) kickLoops() {
	if !s.loopsStarted.Load() {
		return
	}
	for _, k := range s.loopKicks {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

// households lists the households the module loops run for.
func (s *Server) households(ctx context.Context) ([]int64, error) {
	if s.Households != nil {
		return s.Households(ctx)
	}
	if s.HouseholdID == 0 {
		return nil, nil
	}
	return []int64{s.HouseholdID}, nil
}

func (s *Server) runLoop(ctx context.Context, id string, l module.Loop, kick <-chan struct{}) {
	// ran holds the households the loop last ran for; one missing from it
	// (new, or its module was off) runs on the next kick too.
	ran := map[int64]bool{}
	run := func(force bool) {
		hhs, err := s.households(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error(l.Name, slog.String("module", id), slog.Any("err", err))
			}
			return
		}
		next := make(map[int64]bool, len(hhs))
		for _, hh := range hhs {
			hctx := withHousehold(ctx, hh)
			if !s.moduleOn(hctx, id) {
				continue
			}
			next[hh] = true
			if !force && ran[hh] {
				continue
			}
			if err := l.Run(hctx, hh); err != nil && ctx.Err() == nil {
				s.Log.Error(l.Name, slog.String("module", id), slog.Int64("household", hh), slog.Any("err", err))
			}
		}
		ran = next
	}
	run(true)
	t := time.NewTicker(l.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run(true)
		case <-kick:
			// Only a module that was off runs early.
			run(false)
		}
	}
}

// Money: My money, a person's own balance opened on the wall with their own
// PIN. A child's PIN opens only their money; it never opens admin or
// unlocks the displays.
type moneyModule struct{ s *Server }

func (moneyModule) ID() string   { return modMoney }
func (moneyModule) Name() string { return "My money" }
func (moneyModule) Description() string {
	return "Kids open their own balance on the wall display with their own PIN, and ask for money to go to their bank."
}
func (moneyModule) Requires() []string { return nil }
func (moneyModule) DefaultOn() bool    { return true }
func (moneyModule) AdminNav() []module.NavItem {
	return []module.NavItem{{Key: "bank", Label: "Bank", Path: "/admin/bank"}}
}
func (moneyModule) Loops() []module.Loop { return nil }

func (m moneyModule) Routes(r module.Router) {
	s := m.s
	r.Viewer("GET /board/money/{id}", s.handleMoneyPin)
	r.Viewer("POST /board/money/{id}/pin", s.handleMoneyCheck)
	r.Viewer("GET /board/money/{id}/view", s.handleMyMoney)
	r.Viewer("GET /board/money/{id}/history", s.handleMoneyHistory)
	r.Viewer("GET /board/money/{id}/bank", s.handleBankAsk)
	r.Viewer("POST /board/money/{id}/bank", s.handleBankRequest)
	r.Viewer("POST /board/money/{id}/bank/{req}/cancel", s.handleBankCancel)
	r.Admin("GET /admin/people/{id}/money", s.handleAdminMoney)
	r.Admin("POST /admin/people/{id}/bank-balance", s.handleSetBankBalance)
	r.Admin("POST /admin/people/{id}/bank-balance/remove", s.handleRemoveBankBalance)
	r.Admin("GET /admin/people/{id}/money/history", s.handleAdminMoneyHistory)
	r.Admin("POST /admin/people/{id}/bank", s.handlePayOut)
	r.Admin("GET /admin/bank", s.handleBank)
	r.Admin("POST /admin/bank/{id}/approve", s.handleBankApprove)
	r.Admin("POST /admin/bank/{id}/decline", s.handleBankDecline)
}

// Rewards: rewards, claiming them and savings goals. Points themselves are
// part of chores and stay when Rewards is off.
type rewardsModule struct{ s *Server }

func (rewardsModule) ID() string   { return modRewards }
func (rewardsModule) Name() string { return "Rewards" }
func (rewardsModule) Description() string {
	return "Rewards to spend points on, claiming them on the board and savings goals."
}
func (rewardsModule) Requires() []string { return nil }
func (rewardsModule) DefaultOn() bool    { return true }
func (rewardsModule) AdminNav() []module.NavItem {
	return []module.NavItem{{Key: "rewards", Label: "Rewards", Path: "/admin/rewards"}}
}
func (rewardsModule) Loops() []module.Loop { return nil }

func (m rewardsModule) Routes(r module.Router) {
	s := m.s
	r.Viewer("POST /board/rewards/{id}/claim", s.handleRewardClaim)
	r.Viewer("POST /board/people/{id}/goal", s.handleGoalSet)
	r.Viewer("POST /board/people/{id}/goal/clear", s.handleGoalClear)
	r.Viewer("GET /board/redemptions/{id}/review", s.handleClaimReviewSheet)
	r.Viewer("POST /board/redemptions/{id}/approve", s.boardReview(s.Chores.ApproveRedemption, "Approved. Enjoy! 🎁"))
	r.Viewer("POST /board/redemptions/{id}/decline", s.boardReview(s.Chores.DeclineRedemption, "Declined. Points are free again."))
	r.Viewer("POST /board/redemptions/{id}/cancel", s.handleClaimCancel)

	r.Admin("GET /admin/rewards", s.handleRewards)
	r.Admin("GET /admin/rewards/new", s.handleRewardNew)
	r.Admin("POST /admin/rewards", s.handleRewardCreate)
	r.Admin("GET /admin/rewards/{id}", s.handleRewardEdit)
	r.Admin("POST /admin/rewards/{id}", s.handleRewardUpdate)
	r.Admin("POST /admin/rewards/{id}/archive", s.handleRewardArchive)
	r.Admin("POST /admin/rewards/{id}/restore", s.handleRewardRestore)
	r.Admin("POST /admin/rewards/claims/{id}/approve", s.adminReview(s.Chores.ApproveRedemption, "Reward approved."))
	r.Admin("POST /admin/rewards/claims/{id}/decline", s.adminReview(s.Chores.DeclineRedemption, "Reward declined; the points are free again."))
}

// Photos: uploaded photos and the photos folder, shown as the screensaver
// and locked-screen slideshow. The screensaver itself (clock, events,
// chores) stays when Photos is off.
type photosModule struct{ s *Server }

func (photosModule) ID() string   { return modPhotos }
func (photosModule) Name() string { return "Photos" }
func (photosModule) Description() string {
	return "Uploaded photos and a photos folder, shown on the screensaver and the locked screen."
}
func (photosModule) Requires() []string { return nil }
func (photosModule) DefaultOn() bool    { return true }
func (photosModule) AdminNav() []module.NavItem {
	return []module.NavItem{{Key: "photos", Label: "Photos", Path: "/admin/photos"}}
}

func (m photosModule) Loops() []module.Loop {
	if m.s.Photos == nil {
		return nil
	}
	return []module.Loop{{
		Name: "scan photos folder", Every: photoRescanEvery,
		Run: func(ctx context.Context, _ int64) error { return m.s.Photos.Rescan(ctx) },
	}}
}

func (m photosModule) Routes(r module.Router) {
	s := m.s
	r.Viewer("GET /photos/{kind}/{name}", s.handlePhoto)
	r.Viewer("GET /screensaver/photos", s.handleSlides)

	r.Admin("GET /admin/photos", s.handlePhotos)
	r.Admin("POST /admin/photos", s.handlePhotoUpload)
	r.Admin("POST /admin/photos/{id}/delete", s.handlePhotoDelete)
}

// Calendar: ICS feeds, their events on the screensaver and in the board
// calendar. The board calendar stays when Calendar is off, with chores
// only.
type calendarModule struct{ s *Server }

func (calendarModule) ID() string   { return modCalendar }
func (calendarModule) Name() string { return "Calendar" }
func (calendarModule) Description() string {
	return "Calendar feeds, with their events on the screensaver and in the board calendar."
}
func (calendarModule) Requires() []string { return nil }
func (calendarModule) DefaultOn() bool    { return true }
func (calendarModule) AdminNav() []module.NavItem {
	return []module.NavItem{{Key: "calendars", Label: "Calendars", Path: "/admin/calendars"}}
}

func (m calendarModule) Loops() []module.Loop {
	if m.s.Calendars == nil {
		return nil
	}
	return []module.Loop{{
		Name: "refresh calendars", Every: calendar.RefreshEvery,
		Run: m.s.Calendars.RefreshHousehold,
	}}
}

func (m calendarModule) Routes(r module.Router) {
	s := m.s
	r.Admin("GET /admin/calendars", s.handleCalendars)
	r.Admin("POST /admin/calendars", s.handleCalendarAdd)
	r.Admin("POST /admin/calendars/refresh", s.handleCalendarRefresh)
	r.Admin("POST /admin/calendars/{id}/delete", s.handleCalendarDelete)
}
