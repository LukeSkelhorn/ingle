package server

import (
	"errors"
	"net/http"

	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

type rewardsView struct {
	Rewards  []dbq.Reward
	Archived []dbq.Reward
}

func (s *Server) handleRewards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Household.Rewards(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	archived, err := s.Household.ArchivedRewards(ctx, s.HouseholdID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.view(r, "Rewards", rewardsView{Rewards: list, Archived: archived})
	v.Nav = "rewards"
	s.render(w, r, http.StatusOK, "admin_rewards", v)
}

type rewardForm struct {
	ID          int64
	Title       string
	Emoji       emojiField
	Description string
	CostPoints  int64
	Money       string
	Archived    bool
}

func newRewardForm() rewardForm { return rewardForm{Emoji: newEmojiField(emoji.Reward, "")} }

func rewardFormFor(rw dbq.Reward) rewardForm {
	return rewardForm{
		ID: rw.ID, Title: rw.Title, Emoji: newEmojiField(emoji.Reward, rw.Emoji), Description: rw.Description,
		CostPoints: rw.CostPoints, Money: formatMoneyInput(rw.CostPence), Archived: rw.ArchivedAt != nil,
	}
}

func (s *Server) renderReward(w http.ResponseWriter, r *http.Request, status int, f rewardForm, errs valid.Errors, flash string) {
	title := "Add a reward"
	if f.ID != 0 {
		title = f.Title
	}
	v := s.view(r, title, f)
	v.Nav = "rewards"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_reward", v)
}

func (s *Server) handleRewardNew(w http.ResponseWriter, r *http.Request) {
	f := newRewardForm()
	f.Money = "0.00"
	s.renderReward(w, r, http.StatusOK, f, nil, "")
}

func (s *Server) loadReward(w http.ResponseWriter, r *http.Request) (dbq.Reward, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return dbq.Reward{}, false
	}
	rw, err := s.Household.Reward(r.Context(), s.HouseholdID, id)
	if errors.Is(err, household.ErrNotFound) {
		http.NotFound(w, r)
		return dbq.Reward{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return dbq.Reward{}, false
	}
	return rw, true
}

func (s *Server) handleRewardEdit(w http.ResponseWriter, r *http.Request) {
	rw, ok := s.loadReward(w, r)
	if !ok {
		return
	}
	s.renderReward(w, r, http.StatusOK, rewardFormFor(rw), nil, "")
}

func (s *Server) readRewardForm(w http.ResponseWriter, r *http.Request, f *rewardForm) (household.RewardInput, *form, error) {
	if err := parseForm(w, r); err != nil {
		return household.RewardInput{}, nil, err
	}
	fm := newForm(r)
	f.Title = fm.str("title")
	f.Emoji.read(fm)
	f.Description = fm.str("description")
	f.CostPoints = fm.int("cost_points", "Enter a whole number of points.")
	f.Money = fm.str("money")
	pence, err := s.Locale.ParseMoney(f.Money)
	if err != nil {
		fm.errs.Add("money", "Enter an amount like 2.50.")
	}
	return household.RewardInput{Title: f.Title, Emoji: f.Emoji.Value, Description: f.Description, CostPoints: f.CostPoints, CostPence: pence}, fm, nil
}

func (s *Server) handleRewardCreate(w http.ResponseWriter, r *http.Request) {
	f := newRewardForm()
	in, fm, err := s.readRewardForm(w, r, &f)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !fm.ok() {
		s.renderReward(w, r, http.StatusUnprocessableEntity, f, fm.errs, "")
		return
	}
	if e, ok := f.Emoji.offerSuggestion(f.Title); ok {
		s.renderReward(w, r, http.StatusOK, f, nil, suggestionFlash(e, "reward"))
		return
	}
	_, err = s.Household.CreateReward(r.Context(), s.HouseholdID, actor(r).Person.ID, in)
	if errs, ok := validationErrors(err); ok {
		s.renderReward(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/rewards")
}

func (s *Server) handleRewardUpdate(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadReward(w, r)
	if !ok {
		return
	}
	f := rewardFormFor(cur)
	in, fm, err := s.readRewardForm(w, r, &f)
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !fm.ok() {
		s.renderReward(w, r, http.StatusUnprocessableEntity, f, fm.errs, "")
		return
	}
	if e, ok := f.Emoji.offerSuggestion(f.Title); ok {
		s.renderReward(w, r, http.StatusOK, f, nil, suggestionFlash(e, "reward"))
		return
	}
	rw, err := s.Household.UpdateReward(r.Context(), s.HouseholdID, actor(r).Person.ID, cur.ID, in)
	if errs, ok := validationErrors(err); ok {
		s.renderReward(w, r, http.StatusUnprocessableEntity, f, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderReward(w, r, http.StatusOK, rewardFormFor(rw), nil, "Saved.")
}

func (s *Server) handleRewardArchive(w http.ResponseWriter, r *http.Request) {
	rw, ok := s.loadReward(w, r)
	if !ok {
		return
	}
	if err := s.Household.ArchiveReward(r.Context(), s.HouseholdID, actor(r).Person.ID, rw.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/rewards")
}

func (s *Server) handleRewardRestore(w http.ResponseWriter, r *http.Request) {
	rw, ok := s.loadReward(w, r)
	if !ok {
		return
	}
	if err := s.Household.RestoreReward(r.Context(), s.HouseholdID, actor(r).Person.ID, rw.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/admin/rewards/"+itoa(rw.ID))
}
