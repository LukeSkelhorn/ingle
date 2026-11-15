package server

import (
	"net/http"
	"strconv"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/locale"
)

// interestForm is Admin › Settings › Bank of Mum and Dad.
type interestForm struct {
	On                  bool
	Rate                string // % a month, as typed
	Money, Points       bool
	CapMoney, CapPoints string // "" for no limit

	filled bool
}

// Yearly is about what the rate comes to over a year, or "" if it isn't a
// rate yet.
func (f interestForm) Yearly() string {
	bp, err := chores.ParseRate(f.Rate)
	if err != nil || bp <= 0 || bp > chores.MaxInterestBP {
		return ""
	}
	return chores.YearlyRate(bp)
}

func newInterestForm(in chores.Interest) interestForm {
	f := interestForm{On: in.On(), Money: in.Money, Points: in.Points, filled: true}
	if in.RateBP > 0 {
		f.Rate = chores.FormatRate(in.RateBP)
	}
	if in.CapPence > 0 {
		f.CapMoney = locale.FormatMoneyInput(in.CapPence)
	}
	if in.CapPoints > 0 {
		f.CapPoints = strconv.FormatInt(in.CapPoints, 10)
	}
	return f
}

func (s *Server) handleInterestUpdate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	fm := newForm(r)
	data := interestForm{
		On: fm.checked("interest_on"), Rate: fm.str("interest_rate"),
		Money: fm.checked("interest_money"), Points: fm.checked("interest_points"),
		CapMoney: fm.str("interest_cap_money"), CapPoints: fm.str("interest_cap_points"),
		filled: true,
	}
	in := chores.InterestInput{On: data.On, Money: data.Money, Points: data.Points}
	if data.Rate != "" {
		bp, err := chores.ParseRate(data.Rate)
		if err != nil && data.On {
			fm.errs.Add("interest_rate", "Enter a rate like 1 or 0.5.")
		}
		in.RateBP = bp
	}
	if data.CapMoney != "" {
		pence, err := s.Locale.ParseMoney(data.CapMoney)
		if err != nil {
			fm.errs.Add("interest_cap_money", "Enter an amount like 0.50, or leave it blank for no limit.")
		}
		in.CapPence = pence
	}
	in.CapPoints = fm.int("interest_cap_points", "Enter a whole number of points, or leave it blank for no limit.")

	errs := fm.errs
	if fm.ok() {
		err := s.Chores.SaveInterest(r.Context(), hhOf(r), actor(r).Person.ID, in)
		if e, ok := validationErrors(err); ok {
			errs = e
		} else if err != nil {
			s.serverError(w, r, err)
			return
		} else {
			flash := "Interest is off."
			if in.On {
				flash = "Interest saved. It's paid on the 1st of each month."
			}
			s.settingsSaved(w, r, flash)
			return
		}
	}
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := s.settingsForm(set)
	f.Interest = data
	s.renderSettings(w, r, http.StatusUnprocessableEntity, f, errs, "")
}
