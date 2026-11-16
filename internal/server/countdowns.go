package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/module"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

const modCountdowns = "countdowns"

// stripCountdowns is how many countdowns the board's strip shows.
const stripCountdowns = 3

// Countdowns: days to go until birthdays, holidays and big days, on the
// board, its Countdowns view and the screensaver. Grown-ups add them in
// admin; a child with "Can add countdowns" can add their own on a display
// after their PIN.
type countdownsModule struct{ s *Server }

func (countdownsModule) ID() string   { return modCountdowns }
func (countdownsModule) Name() string { return "Countdowns" }
func (countdownsModule) Description() string {
	return "Count down the days to birthdays, holidays and big days on the board."
}
func (countdownsModule) Requires() []string { return nil }
func (countdownsModule) DefaultOn() bool    { return true }
func (countdownsModule) AdminNav() []module.NavItem {
	return []module.NavItem{{Key: "countdowns", Label: "Countdowns", Path: "/admin/countdowns"}}
}
func (countdownsModule) Loops() []module.Loop { return nil }

func (m countdownsModule) Routes(r module.Router) {
	s := m.s
	r.Viewer("GET /board/countdowns", s.handleCountdownsView)
	r.Viewer("GET /board/countdowns/add", s.handleCountdownWho)
	r.Viewer("GET /board/countdowns/by/{id}", s.handleCountdownPin)
	r.Viewer("POST /board/countdowns/by/{id}/pin", s.handleCountdownCheck)
	r.Viewer("GET /board/countdowns/by/{id}/mine", s.handleCountdownMine)
	r.Viewer("POST /board/countdowns/by/{id}", s.handleKidCountdownAdd)
	r.Viewer("GET /board/countdowns/by/{id}/edit/{cid}", s.handleKidCountdownEdit)
	r.Viewer("POST /board/countdowns/by/{id}/edit/{cid}", s.handleKidCountdownUpdate)
	r.Viewer("POST /board/countdowns/by/{id}/delete/{cid}", s.handleKidCountdownDelete)

	r.Admin("GET /admin/countdowns", s.handleCountdowns)
	r.Admin("GET /admin/countdowns/new", s.handleCountdownNew)
	r.Admin("POST /admin/countdowns", s.handleCountdownCreate)
	r.Admin("GET /admin/countdowns/{id}", s.handleCountdownEdit)
	r.Admin("POST /admin/countdowns/{id}", s.handleCountdownUpdate)
	r.Admin("POST /admin/countdowns/{id}/delete", s.handleCountdownDelete)
}

// countdowns is the household's upcoming countdowns and birthdays, soonest
// first, or none while the module is off.
func (s *Server) countdowns(r *http.Request) ([]chores.Countdown, error) {
	if !s.moduleOn(r.Context(), modCountdowns) {
		return nil, nil
	}
	return s.Chores.Countdowns(r.Context(), hhOf(r), s.boardDay(r), false)
}

// Admin › Countdowns.

type countdownsAdminView struct {
	Upcoming, Passed []chores.Countdown
}

func (s *Server) handleCountdowns(w http.ResponseWriter, r *http.Request) {
	all, err := s.Chores.Countdowns(r.Context(), hhOf(r), s.boardDay(r), true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var v countdownsAdminView
	for _, c := range all {
		if c.Passed() {
			v.Passed = append(v.Passed, c)
		} else {
			v.Upcoming = append(v.Upcoming, c)
		}
	}
	page := s.view(r, "Countdowns", v)
	page.Nav = "countdowns"
	page.Flash = r.URL.Query().Get("flash")
	s.render(w, r, http.StatusOK, "admin_countdowns", page)
}

// countdownForm is the countdown form, in admin or (for a child) in the
// board's sheet.
type countdownForm struct {
	ID        int64
	Title     string
	Emoji     emojiField
	OnDate    string
	Yearly    bool
	PeopleIDs []int64      // who it's for, if anyone
	People    []dbq.Person // to choose from (admin only)
	Min       string       // the earliest day a one-off can be, YYYY-MM-DD
}

func newCountdownForm() countdownForm {
	return countdownForm{Emoji: newEmojiField(emoji.Countdown, "")}
}

func (s *Server) countdownFormFor(r *http.Request, c dbq.Countdown) (countdownForm, error) {
	people, err := s.Chores.CountdownPeople(r.Context(), hhOf(r), c.ID)
	return countdownForm{
		ID: c.ID, Title: c.Title, Emoji: newEmojiField(emoji.Countdown, c.Emoji), OnDate: c.OnDate, Yearly: c.Yearly == 1,
		PeopleIDs: people,
	}, err
}

// readCountdownForm reads the form; withPerson takes "For" too (admin).
// Without it, who it's for stays as in f.
func (s *Server) readCountdownForm(w http.ResponseWriter, r *http.Request, f *countdownForm, withPerson bool) (chores.CountdownInput, error) {
	if err := parseForm(w, r); err != nil {
		return chores.CountdownInput{}, err
	}
	fm := newForm(r)
	f.Title = fm.str("title")
	f.Emoji.read(fm)
	f.OnDate = fm.str("on_date")
	f.Yearly = fm.checked("yearly")
	in := chores.CountdownInput{Title: f.Title, Emoji: f.Emoji.Value, OnDate: f.OnDate, Yearly: f.Yearly}
	if withPerson {
		f.PeopleIDs = fm.ids("people_ids")
	}
	in.PeopleIDs = f.PeopleIDs // a child keeps who it's for as it was
	return in, nil
}

func (s *Server) renderCountdown(w http.ResponseWriter, r *http.Request, status int, f countdownForm, errs valid.Errors, flash string) {
	title := "Add a countdown"
	if f.ID != 0 {
		title = f.Title
	}
	people, err := s.Household.People(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f.People = people
	f.Min = s.boardDay(r).Format(chores.DateLayout)
	v := s.view(r, title, f)
	v.Nav = "countdowns"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_countdown", v)
}

func (s *Server) handleCountdownNew(w http.ResponseWriter, r *http.Request) {
	s.renderCountdown(w, r, http.StatusOK, newCountdownForm(), nil, "")
}

// loadCountdown is the countdown in the path (wildcard name).
func (s *Server) loadCountdown(w http.ResponseWriter, r *http.Request, name string) (dbq.Countdown, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return dbq.Countdown{}, false
	}
	c, err := s.Chores.Countdown(r.Context(), hhOf(r), id)
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return dbq.Countdown{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return dbq.Countdown{}, false
	}
	return c, true
}

func (s *Server) handleCountdownEdit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCountdown(w, r, "id")
	if !ok {
		return
	}
	f, err := s.countdownFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderCountdown(w, r, http.StatusOK, f, nil, "")
}

func (s *Server) handleCountdownCreate(w http.ResponseWriter, r *http.Request) {
	f := newCountdownForm()
	in, err := s.readCountdownForm(w, r, &f, true)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if e, ok := f.Emoji.offerSuggestion(f.Title); ok {
		s.renderCountdown(w, r, http.StatusUnprocessableEntity, f, nil, suggestionFlash(e, "countdown"))
		return
	}
	in.Emoji = f.Emoji.Value
	if _, err := s.Chores.AddCountdown(r.Context(), hhOf(r), actor(r).Person.ID, in, s.boardDay(r)); err != nil {
		if errs, ok := validationErrors(err); ok {
			s.renderCountdown(w, r, http.StatusUnprocessableEntity, f, errs, "")
			return
		}
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/countdowns?"+url.Values{"flash": {"Added " + in.Title + "."}}.Encode())
}

func (s *Server) handleCountdownUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCountdown(w, r, "id")
	if !ok {
		return
	}
	f, err := s.countdownFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in, err := s.readCountdownForm(w, r, &f, true)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.Chores.UpdateCountdown(r.Context(), hhOf(r), actor(r).Person.ID, c.ID, in, s.boardDay(r)); err != nil {
		if errs, ok := validationErrors(err); ok {
			s.renderCountdown(w, r, http.StatusUnprocessableEntity, f, errs, "")
			return
		}
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/countdowns?"+url.Values{"flash": {"Saved " + in.Title + "."}}.Encode())
}

func (s *Server) handleCountdownDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCountdown(w, r, "id")
	if !ok {
		return
	}
	if err := s.Chores.RemoveCountdown(r.Context(), hhOf(r), actor(r).Person.ID, c.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/countdowns?"+url.Values{"flash": {"Deleted " + c.Title + "."}}.Encode())
}

// The board's Countdowns view.

type countdownsBoardView struct {
	Countdowns []chores.Countdown
	CanAdd     bool // someone can add one here: a child with the tick
	Adult      bool // a signed-in adult, who adds them in admin
}

func (s *Server) handleCountdownsView(w http.ResponseWriter, r *http.Request) {
	cs, err := s.countdowns(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	kids, err := s.countdownKids(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "countdowns_view", countdownsBoardView{
		Countdowns: cs, CanAdd: len(kids) > 0, Adult: identityFrom(r.Context()).Session != nil,
	})
}

// countdownKids is the children who may add countdowns.
func (s *Server) countdownKids(r *http.Request) ([]dbq.Person, error) {
	people, err := s.Household.People(r.Context(), hhOf(r))
	if err != nil {
		return nil, err
	}
	var out []dbq.Person
	for _, p := range people {
		if p.Kind == household.KindChild && p.CanAddCountdowns == 1 {
			out = append(out, p)
		}
	}
	return out, nil
}

// handleCountdownWho is the "Who's adding one?" sheet.
func (s *Server) handleCountdownWho(w http.ResponseWriter, r *http.Request) {
	kids, err := s.countdownKids(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "countdown_who", struct {
		People []dbq.Person
		Adult  bool
	}{kids, identityFrom(r.Context()).Session != nil})
}

// countdownKid is the child in the path, if they may add countdowns.
func (s *Server) countdownKid(w http.ResponseWriter, r *http.Request) (dbq.Person, bool) {
	p, ok := s.moneyPerson(w, r)
	if !ok {
		return dbq.Person{}, false
	}
	if p.Kind != household.KindChild || p.CanAddCountdowns != 1 {
		http.Error(w, "A grown-up can let you add countdowns in Admin › People.", http.StatusForbidden)
		return dbq.Person{}, false
	}
	return p, true
}

// countdownKidAccess is the child in the path with a good PIN check (or a
// signed-in adult off the displays). Without one it shows the PIN pad
// again.
func (s *Server) countdownKidAccess(w http.ResponseWriter, r *http.Request) (dbq.Person, string, bool) {
	p, ok := s.countdownKid(w, r)
	if !ok {
		return dbq.Person{}, "", false
	}
	token, ok := s.moneyAccess(r, p)
	if !ok {
		s.renderCountdownPin(w, r, p, true)
		return dbq.Person{}, "", false
	}
	return p, token, true
}

func (s *Server) renderCountdownPin(w http.ResponseWriter, r *http.Request, p dbq.Person, expired bool) {
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartial(w, r, "countdown_pin", moneyPinView{
		Person: p, NotDisplay: identityFrom(r.Context()).Device == nil, Expired: expired,
	})
}

func (s *Server) handleCountdownPin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.countdownKid(w, r)
	if !ok {
		return
	}
	if moneyAdult(r) {
		s.renderCountdownMine(w, r, http.StatusOK, p, "", newCountdownForm(), nil, "")
		return
	}
	s.renderCountdownPin(w, r, p, false)
}

// handleCountdownCheck checks the PIN typed on the pad, as for My money.
// Errors are JSON for the pad.
func (s *Server) handleCountdownCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.countdownKid(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token, err := s.Auth.CheckMoneyPIN(r.Context(), identityFrom(r.Context()).Device, p.ID, r.PostFormValue("pin"), s.clientAddr(r))
	switch {
	case errors.Is(err, auth.ErrLockedOut):
		keypadError(w, http.StatusTooManyRequests, "Too many tries. Try again in 5 minutes.")
		return
	case errors.Is(err, auth.ErrBadCredentials):
		keypadError(w, http.StatusUnauthorized, "Wrong PIN. Try again.")
		return
	case errors.Is(err, auth.ErrNotDisplay):
		keypadError(w, http.StatusForbidden, "Adding countdowns opens on a household display.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.renderCountdownMine(w, r, http.StatusOK, p, token, newCountdownForm(), nil, "")
}

// countdownMineView is a child's sheet after their PIN: the form to add a
// countdown (or change one of theirs), and the ones they've added.
type countdownMineView struct {
	Person dbq.Person
	Token  string
	Form   countdownForm
	Errors valid.Errors
	Mine   []chores.Countdown
	Done   string // what was just saved, for the message
}

// Base is where the sheet's forms go.
func (v countdownMineView) Base() string { return "/board/countdowns/by/" + itoa(v.Person.ID) }

// Q is the token as a query string, "" for a signed-in adult.
func (v countdownMineView) Q() string {
	if v.Token == "" {
		return ""
	}
	return "?" + url.Values{"t": {v.Token}}.Encode()
}

func (s *Server) renderCountdownMine(w http.ResponseWriter, r *http.Request, status int, p dbq.Person, token string, f countdownForm, errs valid.Errors, done string) {
	all, err := s.Chores.Countdowns(r.Context(), hhOf(r), s.boardDay(r), false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f.Min = s.boardDay(r).Format(chores.DateLayout)
	v := countdownMineView{Person: p, Token: token, Form: f, Errors: errs, Done: done}
	for _, c := range all {
		if c.ID != 0 && c.AddedBy == p.ID {
			v.Mine = append(v.Mine, c)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderPartialStatus(w, r, status, "countdown_mine", v)
}

func (s *Server) handleCountdownMine(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.countdownKidAccess(w, r)
	if !ok {
		return
	}
	s.renderCountdownMine(w, r, http.StatusOK, p, token, newCountdownForm(), nil, "")
}

// countdownsChanged tells this board (the toast and its Countdowns view)
// and the others that the countdowns changed.
func (s *Server) countdownsChanged(w http.ResponseWriter, r *http.Request, message string) {
	b, err := json.Marshal(map[string]any{"toast": toast{Message: message}, "ingle:countdowns": true})
	if err == nil {
		w.Header().Set("HX-Trigger", asciiJSON(b))
	}
	s.events.publish(hhOf(r), boardClient(r.Header.Get(boardClientHeader)))
}

func (s *Server) handleKidCountdownAdd(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.countdownKidAccess(w, r)
	if !ok {
		return
	}
	f := newCountdownForm()
	in, err := s.readCountdownForm(w, r, &f, false)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if _, err := s.Chores.AddCountdown(r.Context(), hhOf(r), p.ID, in, s.boardDay(r)); err != nil {
		if errs, ok := validationErrors(err); ok {
			s.renderCountdownMine(w, r, http.StatusUnprocessableEntity, p, token, f, errs, "")
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.countdownsChanged(w, r, "Countdown added 🎉")
	s.renderCountdownMine(w, r, http.StatusOK, p, token, newCountdownForm(), nil, "Added "+in.Title+".")
}

// kidCountdown is the countdown in the path, if p added it.
func (s *Server) kidCountdown(w http.ResponseWriter, r *http.Request, p dbq.Person) (dbq.Countdown, bool) {
	c, ok := s.loadCountdown(w, r, "cid")
	if !ok {
		return dbq.Countdown{}, false
	}
	if c.AddedBy == nil || *c.AddedBy != p.ID {
		http.Error(w, "Only whoever added a countdown (or a grown-up) can change it.", http.StatusForbidden)
		return dbq.Countdown{}, false
	}
	return c, true
}

func (s *Server) handleKidCountdownEdit(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.countdownKidAccess(w, r)
	if !ok {
		return
	}
	c, ok := s.kidCountdown(w, r, p)
	if !ok {
		return
	}
	f, err := s.countdownFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderCountdownMine(w, r, http.StatusOK, p, token, f, nil, "")
}

func (s *Server) handleKidCountdownUpdate(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.countdownKidAccess(w, r)
	if !ok {
		return
	}
	c, ok := s.kidCountdown(w, r, p)
	if !ok {
		return
	}
	f, err := s.countdownFormFor(r, c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in, err := s.readCountdownForm(w, r, &f, false)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.Chores.UpdateCountdown(r.Context(), hhOf(r), p.ID, c.ID, in, s.boardDay(r)); err != nil {
		if errs, ok := validationErrors(err); ok {
			s.renderCountdownMine(w, r, http.StatusUnprocessableEntity, p, token, f, errs, "")
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.countdownsChanged(w, r, "Countdown saved")
	s.renderCountdownMine(w, r, http.StatusOK, p, token, newCountdownForm(), nil, "Saved "+in.Title+".")
}

func (s *Server) handleKidCountdownDelete(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.countdownKidAccess(w, r)
	if !ok {
		return
	}
	c, ok := s.kidCountdown(w, r, p)
	if !ok {
		return
	}
	if err := s.Chores.RemoveCountdown(r.Context(), hhOf(r), p.ID, c.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.countdownsChanged(w, r, "Countdown deleted")
	s.renderCountdownMine(w, r, http.StatusOK, p, token, newCountdownForm(), nil, "Deleted "+c.Title+".")
}
