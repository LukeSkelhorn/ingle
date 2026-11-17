package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

type matchPanel struct {
	Current              *chores.MatchOffer
	AddMoney, PerMoney   string
	AddPoints, PerPoints string
	CapMoney, CapPoints  string
	Typed                bool
}

func (s *Server) loadMatchPanel(r *http.Request, f *personForm) error {
	if f.Kind != "child" || !s.moduleOn(r.Context(), modRewards) {
		return nil
	}
	o, err := s.Chores.MatchOffer(r.Context(), hhOf(r), f.ID)
	if err != nil {
		return err
	}
	f.Match.Current = o
	if f.Match.Typed {
		return nil
	}
	f.Match.PerMoney, f.Match.PerPoints = formatMoneyInput(100), "10"
	if o == nil {
		return nil
	}
	if o.Money() {
		f.Match.AddMoney, f.Match.PerMoney = formatMoneyInput(o.AddPence), formatMoneyInput(o.PerPence)
	}
	if o.Points() {
		f.Match.AddPoints, f.Match.PerPoints = strconv.FormatInt(o.AddPoints, 10), strconv.FormatInt(o.PerPoints, 10)
	}
	if o.CapPence > 0 {
		f.Match.CapMoney = formatMoneyInput(o.CapPence)
	}
	if o.CapPoints > 0 {
		f.Match.CapPoints = strconv.FormatInt(o.CapPoints, 10)
	}
	return nil
}

func (s *Server) handleMatchSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	money := s.moduleOn(r.Context(), modMoney)
	field := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	panel := matchPanel{
		AddMoney: field("match_add_money"), PerMoney: field("match_per_money"),
		AddPoints: field("match_add_points"), PerPoints: field("match_per_points"),
		CapMoney: field("match_cap_money"), CapPoints: field("match_cap_points"),
		Typed: true,
	}
	errs := valid.Errors{}
	parseMoney := func(v, key, msg string) int64 {
		if v == "" || !money {
			return 0
		}
		pence, err := s.Locale.ParseMoney(v)
		if err != nil || pence < 0 {
			errs.Add(key, msg)
		}
		return pence
	}
	parseInt := func(v, key, msg string) int64 {
		if v == "" {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs.Add(key, msg)
		}
		return n
	}
	const moneyMsg, pointsMsg = "Enter amounts like 0.10 and 1.00.", "Enter whole numbers of points."
	in := chores.MatchInput{
		AddPence:  parseMoney(panel.AddMoney, "match_money", moneyMsg),
		PerPence:  parseMoney(panel.PerMoney, "match_money", moneyMsg),
		AddPoints: parseInt(panel.AddPoints, "match_points", pointsMsg),
		PerPoints: parseInt(panel.PerPoints, "match_points", pointsMsg),
		CapPence:  parseMoney(panel.CapMoney, "match_cap_money", "Enter an amount like 5.00, or leave it blank for no limit."),
		CapPoints: parseInt(panel.CapPoints, "match_cap_points", "Enter a whole number of points, or leave it blank for no limit."),
	}
	again := func(errs valid.Errors) {
		f := s.personFormFor(r, p)
		f.Match = panel
		s.renderPerson(w, r, http.StatusUnprocessableEntity, f, errs, "")
	}
	if errs.Err() != nil {
		again(errs)
		return
	}
	err := s.Chores.SaveMatchOffer(r.Context(), hhOf(r), p.ID, adult.ID, in)
	if e, ok := validationErrors(err); ok {
		again(e)
		return
	}
	if err != nil {
		s.matchFailed(w, r, p.Name, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil,
		fmt.Sprintf("Matching saved. %s will see it on their goal.", p.Name))
}

func (s *Server) handleMatchRemove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPerson(w, r)
	if !ok {
		return
	}
	adult := boardAdult(r)
	if adult == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.Chores.RemoveMatchOffer(r.Context(), hhOf(r), p.ID, adult.ID); err != nil {
		s.matchFailed(w, r, p.Name, err)
		return
	}
	s.renderPerson(w, r, http.StatusOK, s.personFormFor(r, p), nil,
		fmt.Sprintf("Stopped matching for %s. What they were matched stays theirs.", p.Name))
}

func (s *Server) matchFailed(w http.ResponseWriter, r *http.Request, name string, err error) {
	switch {
	case errors.Is(err, chores.ErrNotFound):
		http.Error(w, "only children can be matched; restore "+name+" first if they're archived", http.StatusConflict)
	case errors.Is(err, chores.ErrNotAdult):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		s.serverError(w, r, err)
	}
}
