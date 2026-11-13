package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// holidayForm is Admin › Settings › Holiday mode: the holidays that haven't
// finished, and the form for adding one.
type holidayForm struct {
	Today    string
	Holidays []chores.Holiday
	People   []dbq.Person
	// The add form's values: From defaults to today ("from now").
	From, To string
	PersonID int64 // 0: everyone
}

// loadHolidays fills in f.Holidays with the list for the household.
func (s *Server) loadHolidays(r *http.Request, f *holidayForm) error {
	day := s.boardDay(r)
	f.Today = day.Format(chores.DateLayout)
	if f.From == "" {
		f.From = f.Today
	}
	var err error
	if f.Holidays, err = s.Chores.Holidays(r.Context(), hhOf(r), day); err != nil {
		return err
	}
	f.People, err = s.Household.People(r.Context(), hhOf(r))
	return err
}

func (s *Server) handleHolidayAdd(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in := chores.HolidayInput{From: r.PostFormValue("holiday_from"), To: r.PostFormValue("holiday_to")}
	hf := holidayForm{From: in.From, To: in.To}
	if v := r.PostFormValue("holiday_person"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "bad person", http.StatusBadRequest)
			return
		}
		in.PersonID, hf.PersonID = &id, id
	}
	_, err := s.Chores.AddHoliday(r.Context(), hhOf(r), actor(r).Person.ID, in, s.boardDay(r))
	if errs, ok := validationErrors(err); ok {
		set, err := s.Settings.Get(r.Context(), hhOf(r))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		f := s.settingsForm(set)
		f.Holiday = hf
		s.renderSettings(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.settingsSaved(w, r, "Holiday added. Chores are paused on those days.")
}

func (s *Server) handleHolidayEnd(w http.ResponseWriter, r *http.Request) {
	s.changeHoliday(w, r, s.Chores.EndHoliday, "Holiday ended. Chores start again today.")
}

func (s *Server) handleHolidayRemove(w http.ResponseWriter, r *http.Request) {
	s.changeHoliday(w, r, s.Chores.RemoveHoliday, "Holiday removed.")
}

func (s *Server) changeHoliday(w http.ResponseWriter, r *http.Request,
	change func(ctx context.Context, householdID, actorID, id int64, boardDay time.Time) error, flash string,
) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := change(r.Context(), hhOf(r), actor(r).Person.ID, id, s.boardDay(r))
	if errors.Is(err, chores.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.settingsSaved(w, r, flash)
}
