package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

type peopleView struct {
	People   []dbq.Person
	Archived []dbq.Person
	// Balances are each active person's points and pence, by person ID.
	Balances map[int64]dbq.BalancesForHouseholdRow
	// Adjust is the open Give / Take points pop-up, nil when closed.
	Adjust *pointsDialog
	// Undo is the adjustment just made from the list, nil for none.
	Undo *peopleUndo
}

// pointsDialog is the Adjust points pop-up on Admin › People.
type pointsDialog struct {
	Person dbq.Person
	Panel  pointsPanel
}

// peopleUndo is the Undo button in the flash after adjusting from the list.
type peopleUndo struct {
	PersonID, EntryID int64
}

// fromPeople is the form value that sends an adjustment (or its undo) back
// to Admin › People instead of the person's page.
const fromPeople = "people"

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	var v peopleView
	if id, err := strconv.ParseInt(r.URL.Query().Get("adjust"), 10, 64); err == nil && id > 0 {
		p, err := s.Household.Person(r.Context(), hhOf(r), id)
		switch {
		case errors.Is(err, household.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		if p.ArchivedAt == nil {
			dir := r.URL.Query().Get("direction")
			if dir != "give" {
				dir = "take"
			}
			v.Adjust = &pointsDialog{Person: p, Panel: pointsPanel{Direction: dir}}
		}
	}
	s.renderPeople(w, r, http.StatusOK, v, nil, "")
}

// renderPeople renders Admin › People, with the Adjust points pop-up open
// when v.Adjust is set.
func (s *Server) renderPeople(w http.ResponseWriter, r *http.Request, status int, v peopleView, errs valid.Errors, flash string) {
	ctx := r.Context()
	var err error
	if v.People, err = s.Household.People(ctx, hhOf(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Archived, err = s.Household.ArchivedPeople(ctx, hhOf(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	balances, err := s.Store.Q.BalancesForHousehold(ctx, hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Balances = make(map[int64]dbq.BalancesForHouseholdRow, len(balances))
	for _, b := range balances {
		v.Balances[b.PersonID] = b
	}
	if v.Adjust != nil {
		if v.Adjust.Panel.Suggestions, err = s.Chores.ReasonSuggestions(ctx, hhOf(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
		b := v.Balances[v.Adjust.Person.ID]
		v.Adjust.Panel.Points = b.Points
	}
	page := s.view(r, "People", v)
	page.Nav = "people"
	page.Errors = errs
	page.Flash = flash
	s.render(w, r, status, "admin_people", page)
}

// personForm is the person edit page: the profile form plus, for adults,
// their PIN and sign-in.
type personForm struct {
	ID         int64 // 0 when creating
	Name       string
	Kind       string
	Colour     string
	AvatarMode string // none, emoji, photo
	Emoji      string
	Photo      string // current photo URL
	SortOrder  int64
	Pictures   bool // picture cards on the board
	Archived   bool

	IsAdult  bool
	HasPIN   bool
	HasLogin bool
	Email    string
	IsMe     bool

	Colours []string
	Emojis  []string

	Goal    int64        // the reward they're saving for, 0 for none
	Rewards []dbq.Reward // to pick a goal from (editing only)

	Points pointsPanel // the Adjust points section (editing only)
	Bank   bankPanel   // a child's Bank balance section (editing only)
}

// pointsPanel is the person page's Points section: their balance, the
// Adjust points form (with what was typed, after an error), the reasons to
// suggest, their recent adjustments, and the one just made, for Undo.
type pointsPanel struct {
	Points, HeldPoints int64
	Pence, HeldPence   int64  // money they can spend, and what's held
	PayOut             string // Move to bank's amount, as typed
	PayAll             bool   // Move to bank's All of it
	Direction          string // take or give
	Amount             string // 1, 2, 5, 10 or custom
	Custom             string
	Reason             string
	Suggestions        []string
	History            []chores.Adjustment
	Undo               int64 // the ledger entry just made, 0 for none
}

// quickAmounts are the Adjust points form's one-tap amounts.
var quickAmounts = []string{"1", "2", "5", "10"}

func (pointsPanel) QuickAmounts() []string { return quickAmounts }

func newPersonForm() personForm {
	return personForm{
		Kind: household.KindChild, Colour: household.Colours[0], AvatarMode: "emoji",
		Emoji: avatar.Emojis[0], Colours: household.Colours, Emojis: avatar.Emojis,
		Points: pointsPanel{Direction: "take", Amount: "5"},
	}
}

func (s *Server) personFormFor(r *http.Request, p dbq.Person) personForm {
	f := newPersonForm()
	f.ID, f.Name, f.Kind, f.Colour, f.SortOrder = p.ID, p.Name, p.Kind, p.Colour, p.SortOrder
	f.Pictures = p.PictureCards == 1
	f.Archived = p.ArchivedAt != nil
	f.IsAdult = p.Kind == household.KindAdult
	f.HasPIN = p.PinHash != nil
	f.HasLogin = p.PasswordHash != nil
	if p.Email != nil {
		f.Email = *p.Email
	}
	f.IsMe = actor(r).Person.ID == p.ID
	if p.GoalRewardID != nil {
		f.Goal = *p.GoalRewardID
	}
	f.AvatarMode, f.Emoji = "none", ""
	switch kind, val := avatar.Parse(p.Avatar); kind {
	case avatar.Emoji:
		f.AvatarMode, f.Emoji = "emoji", val
	case avatar.Photo:
		f.AvatarMode, f.Photo = "photo", "/avatars/"+val
	}
	return f
}

func (s *Server) renderPerson(w http.ResponseWriter, r *http.Request, status int, f personForm, errs valid.Errors, flash string) {
	title := "Add someone"
	if f.ID != 0 {
		title = f.Name
	}
	if f.ID != 0 && !f.Archived {
		// No rewards, no "Saving for" picker.
		if s.moduleOn(r.Context(), modRewards) {
			rewards, err := s.Household.Rewards(r.Context(), hhOf(r))
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			f.Rewards = rewards
		}
		if err := s.loadPointsPanel(r, &f); err != nil {
			s.serverError(w, r, err)
			return
		}
		if err := s.loadBankPanel(r, &f); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	v := s.view(r, title, f)
	v.Nav = "people"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_person", v)
}

func (s *Server) handlePersonNew(w http.ResponseWriter, r *http.Request) {
	s.renderPerson(w, r, http.StatusOK, newPersonForm(), nil, "")
}

func (s *Server) handlePersonEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil, "")
}

func (s *Server) loadPerson(w http.ResponseWriter, r *http.Request) (dbq.Person, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return dbq.Person{}, false
	}
	p, err := s.Household.Person(r.Context(), hhOf(r), id)
	if errors.Is(err, household.ErrNotFound) {
		http.NotFound(w, r)
		return dbq.Person{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return dbq.Person{}, false
	}
	return p, true
}

// readPersonForm parses the (multipart) profile form. A newly uploaded
// photo is saved and returned as newPhoto so the caller can delete it if the
// save fails.
func (s *Server) readPersonForm(w http.ResponseWriter, r *http.Request, f *personForm, current *string) (in household.PersonInput, newPhoto string, errs valid.Errors, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, avatar.MaxUpload+maxFormBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return in, "", valid.Errors{"photo": "That upload was too large (8 MB maximum)."}, nil
	}
	fm := newForm(r)
	f.Name = fm.str("name")
	f.Kind = fm.str("kind")
	f.Colour = fm.str("colour")
	f.AvatarMode = fm.str("avatar_mode")
	f.Emoji = fm.str("emoji")
	f.SortOrder = fm.int("sort_order", "Use a number from 0 to 999.")
	f.Pictures = fm.checked("picture_cards")

	in = household.PersonInput{Name: f.Name, Kind: f.Kind, Colour: f.Colour, SortOrder: f.SortOrder, PictureCards: f.Pictures}
	switch f.AvatarMode {
	case "emoji":
		v, ok := avatar.EmojiValue(f.Emoji)
		if !ok {
			fm.errs.Add("emoji", "Choose an emoji.")
		}
		in.Avatar = &v
	case "photo":
		file, _, ferr := r.FormFile("photo")
		if ferr == nil {
			defer file.Close()
			name, serr := s.Avatars.SavePhoto(file)
			switch {
			case errors.Is(serr, avatar.ErrTooLarge), errors.Is(serr, avatar.ErrNotAnImage):
				fm.errs.Add("photo", serr.Error()+".")
			case serr != nil:
				return in, "", nil, serr
			default:
				v := avatar.PhotoValue(name)
				in.Avatar, newPhoto = &v, name
			}
		} else {
			// No new file: keep the current photo if there is one.
			if kind, _ := avatar.Parse(current); kind == avatar.Photo {
				in.Avatar = current
			} else {
				fm.errs.Add("photo", "Choose a photo to upload.")
			}
		}
	default:
		f.AvatarMode = "none"
	}
	return in, newPhoto, fm.errs, nil
}

func (s *Server) handlePersonCreate(w http.ResponseWriter, r *http.Request) {
	f := newPersonForm()
	in, newPhoto, errs, err := s.readPersonForm(w, r, &f, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(errs) > 0 {
		_ = s.Avatars.Remove(newPhoto)
		s.renderPerson(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	p, err := s.Household.CreatePerson(r.Context(), hhOf(r), actor(r).Person.ID, in)
	if err != nil {
		_ = s.Avatars.Remove(newPhoto)
		if errs, ok := validationErrors(err); ok {
			s.renderPerson(w, r, http.StatusUnprocessableEntity, f, errs, "")
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.fillChores(r)
	redirect(w, r, "/admin/people/"+itoa(p.ID))
}

// fillChores materialises chore occurrences now, so someone just added (or
// back) gets their copies of Everyone chores without waiting for the hourly
// run. A failure is only logged: the hourly run catches up.
func (s *Server) fillChores(r *http.Request) {
	if _, err := s.Chores.Materialise(r.Context(), hhOf(r), s.boardDay(r), chores.Window); err != nil {
		s.Log.ErrorContext(r.Context(), "materialise chores", slog.Any("err", err))
	}
}

func (s *Server) handlePersonUpdate(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	f := s.personFormFor(r, cur)
	in, newPhoto, errs, err := s.readPersonForm(w, r, &f, cur.Avatar)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(errs) > 0 {
		_ = s.Avatars.Remove(newPhoto)
		s.renderPerson(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	p, err := s.Household.UpdatePerson(r.Context(), hhOf(r), actor(r).Person.ID, cur.ID, in)
	if err != nil {
		_ = s.Avatars.Remove(newPhoto)
		if errs, ok := validationErrors(err); ok {
			s.renderPerson(w, r, http.StatusUnprocessableEntity, f, errs, "")
			return
		}
		if msg, ok := conflictMessage(err); ok {
			s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, cur), nil, msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	// The old photo is no longer referenced.
	if kind, name := avatar.Parse(cur.Avatar); kind == avatar.Photo && (p.Avatar == nil || *p.Avatar != *cur.Avatar) {
		_ = s.Avatars.Remove(name)
	}
	flash := "Saved."
	if r.Form.Has("goal") && s.moduleOn(r.Context(), modRewards) {
		if p, err = s.savePersonGoal(r, p, newForm(r).optID("goal")); errors.Is(err, chores.ErrNotFound) {
			flash = "Saved, but that reward has gone, so their goal wasn't changed."
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil, flash)
}

// savePersonGoal sets or clears what p is saving for, if it changed: saving
// the same goal again would restart its celebration.
func (s *Server) savePersonGoal(r *http.Request, p dbq.Person, goal *int64) (dbq.Person, error) {
	ctx := r.Context()
	switch {
	case goal == nil && p.GoalRewardID == nil,
		goal != nil && p.GoalRewardID != nil && *goal == *p.GoalRewardID:
		return p, nil
	case goal == nil:
		if err := s.Chores.ClearGoal(ctx, hhOf(r), p.ID); err != nil {
			return p, err
		}
	default:
		if _, err := s.Chores.SetGoal(ctx, hhOf(r), p.ID, *goal); err != nil {
			return p, err
		}
	}
	return s.Household.Person(ctx, hhOf(r), p.ID)
}

func (s *Server) handlePersonArchive(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	err := s.Household.ArchivePerson(r.Context(), hhOf(r), actor(r).Person.ID, p.ID)
	if msg, ok := conflictMessage(err); ok {
		s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, p), nil, msg)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.Calendars != nil {
		// Their calendars are the family's now.
		s.Calendars.ForgetPerson(hhOf(r), p.ID)
	}
	if actor(r).Person.ID == p.ID {
		// Archiving yourself ends your own session.
		s.clearCookie(w, r, sessionCookie)
		redirect(w, r, "/")
		return
	}
	redirect(w, r, "/admin/people")
}

func (s *Server) handlePersonRestore(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	if err := s.Household.RestorePerson(r.Context(), hhOf(r), actor(r).Person.ID, p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fillChores(r)
	redirect(w, r, "/admin/people/"+itoa(p.ID))
}

// credentialAction handles the PIN and sign-in forms on the person page.
func (s *Server) credentialAction(w http.ResponseWriter, r *http.Request, flash string, fn func(p dbq.Person, f *form) error) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	err := fn(p, f)
	if !f.ok() {
		s.renderPerson(w, r, http.StatusUnprocessableEntity, s.personFormFor(r, p), f.errs, "")
		return
	}
	if errs, ok := validationErrors(err); ok {
		s.renderPerson(w, r, http.StatusUnprocessableEntity, s.personFormFor(r, p), errs, "")
		return
	}
	if msg, ok := conflictMessage(err); ok {
		s.renderPerson(w, r, http.StatusConflict, s.personFormFor(r, p), nil, msg)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p, err = s.Household.Person(r.Context(), hhOf(r), p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil, flash)
}

// errKidPINOff: a child's PIN only opens My money, so it needs that module.
var errKidPINOff = errors.New("my money is off")

func (s *Server) handlePersonSetPIN(w http.ResponseWriter, r *http.Request) {
	s.credentialAction(w, r, "PIN saved.", func(p dbq.Person, f *form) error {
		if p.Kind != household.KindAdult && !s.moduleOn(r.Context(), modMoney) {
			return errKidPINOff
		}
		if f.str("pin") != f.str("pin_confirm") {
			f.errs.Add("pin_confirm", "The PINs don't match.")
			return nil
		}
		return s.Household.SetPIN(r.Context(), hhOf(r), actor(r).Person.ID, p.ID, f.str("pin"), keepToken(r, p.ID))
	})
}

func (s *Server) handlePersonClearPIN(w http.ResponseWriter, r *http.Request) {
	s.credentialAction(w, r, "PIN removed.", func(p dbq.Person, _ *form) error {
		return s.Household.ClearPIN(r.Context(), hhOf(r), actor(r).Person.ID, p.ID, keepToken(r, p.ID))
	})
}

func (s *Server) handlePersonSetLogin(w http.ResponseWriter, r *http.Request) {
	s.credentialAction(w, r, "Sign-in saved.", func(p dbq.Person, f *form) error {
		if f.raw("password") != f.raw("password_confirm") {
			f.errs.Add("password_confirm", "The passwords don't match.")
			return nil
		}
		return s.Household.SetLogin(r.Context(), hhOf(r), actor(r).Person.ID, p.ID, f.str("email"), f.raw("password"), keepToken(r, p.ID))
	})
}

func (s *Server) handlePersonClearLogin(w http.ResponseWriter, r *http.Request) {
	s.credentialAction(w, r, "Sign-in removed.", func(p dbq.Person, _ *form) error {
		return s.Household.ClearLogin(r.Context(), hhOf(r), actor(r).Person.ID, p.ID, keepToken(r, p.ID))
	})
}

// conflictMessage maps domain rule violations to a message for the page.
func conflictMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, household.ErrHasAssignedChores):
		return "They still have chores assigned. Reassign or archive those chores first.", true
	case errors.Is(err, household.ErrLastAdmin):
		return "At least one adult must be able to sign in, so this can't be done for the last one.", true
	case errors.Is(err, household.ErrNotAdult):
		return "Only adults can have a sign-in.", true
	case errors.Is(err, errKidPINOff):
		return "A child's PIN opens My money. Turn My money on in Admin › Modules first.", true
	case errors.Is(err, household.ErrNotFound):
		return "That person couldn't be found.", true
	}
	return "", false
}
