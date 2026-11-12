// Package household is the admin side of people and rewards: adding and
// editing family members, their sign-in credentials, archiving and restoring,
// and managing rewards. Every change writes an activity row.
package household

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/auth"
	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Person kinds.
const (
	KindAdult = "adult"
	KindChild = "child"
)

// Errors returned by Service.
var (
	ErrNotFound          = errors.New("not found")
	ErrNotAdult          = errors.New("only adults can have a sign-in")
	ErrHasAssignedChores = errors.New("reassign or archive their chores first")
	ErrLastAdmin         = errors.New("at least one adult must be able to sign in")
	ErrSetupDone         = errors.New("household is already set up")
)

// Colours is the swatch palette offered for people. Any #RRGGBB is accepted.
var Colours = []string{
	"#E5484D", "#F76B15", "#FFC53D", "#46A758", "#12A594",
	"#0090FF", "#3E63DD", "#8E4EC6", "#D6409F", "#8D8D8D",
}

var colourRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// PersonInput is the editable part of a person.
type PersonInput struct {
	Name      string
	Kind      string
	Colour    string
	Avatar    *string // "emoji:…" or "photo:…", nil for none
	SortOrder int64
	// PictureCards shows their column as big emoji tiles (children only).
	PictureCards bool
}

// RewardInput is the editable part of a reward.
type RewardInput struct {
	Title       string
	Emoji       string // a single emoji, or "" for none
	Description string
	CostPoints  int64
	CostPence   int64
}

// SetupInput is the first-run form.
type SetupInput struct {
	HouseholdName string
	Person        PersonInput // Kind is forced to adult
	Email         string
	Password      string
	PIN           string
}

// Service manages people and rewards.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New returns a household Service.
func New(st *store.Store) *Service {
	return &Service{st: st, now: time.Now}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// NeedsSetup reports whether no adult can sign in yet.
func (s *Service) NeedsSetup(ctx context.Context, householdID int64) (bool, error) {
	n, err := s.st.Q.CountAdultsWithCredential(ctx, householdID)
	if err != nil {
		return false, fmt.Errorf("count admins: %w", err)
	}
	return n == 0, nil
}

// Setup names the household and creates the first adult with an email,
// password and PIN. It fails with ErrSetupDone once any adult can sign in.
func (s *Service) Setup(ctx context.Context, householdID int64, in SetupInput) (dbq.Person, error) {
	in.Person.Kind = KindAdult
	errs := validatePerson(in.Person)
	name := strings.TrimSpace(in.HouseholdName)
	if name == "" || len([]rune(name)) > 50 {
		errs.Add("household_name", "Enter a household name (up to 50 characters).")
	}
	email, emailErr := normaliseEmail(in.Email)
	if emailErr != "" {
		errs.Add("email", emailErr)
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		errs.Add("password", "Use 10 to 128 characters.")
	}
	if err := auth.ValidatePIN(in.PIN); err != nil {
		errs.Add("pin", "Use 4 to 6 digits.")
	}
	if err := errs.Err(); err != nil {
		return dbq.Person{}, err
	}
	pwHash, err := auth.HashPassword(in.Password)
	if err != nil {
		return dbq.Person{}, err
	}
	pinHash, err := auth.HashPIN(in.PIN)
	if err != nil {
		return dbq.Person{}, err
	}

	var p dbq.Person
	now := s.now()
	err = s.st.WithTx(ctx, func(q *dbq.Queries) error {
		n, err := q.CountAdultsWithCredential(ctx, householdID)
		if err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if n > 0 {
			return ErrSetupDone
		}
		if err := q.UpdateHouseholdName(ctx, dbq.UpdateHouseholdNameParams{Name: name, ID: householdID}); err != nil {
			return fmt.Errorf("name household: %w", err)
		}
		p, err = createPerson(ctx, q, householdID, in.Person, now)
		if err != nil {
			return err
		}
		if err := q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &pinHash, PinLength: auth.PINLength(in.PIN), HouseholdID: householdID, ID: p.ID}); err != nil {
			return fmt.Errorf("set pin: %w", err)
		}
		if err := q.SetPersonLogin(ctx, dbq.SetPersonLoginParams{Email: &email, PasswordHash: &pwHash, HouseholdID: householdID, ID: p.ID}); err != nil {
			return uniqueErr(err)
		}
		if err := log(ctx, q, householdID, p.ID, "household.setup", "household", householdID, now); err != nil {
			return err
		}
		p, err = q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: p.ID})
		return err
	})
	return p, err
}

// People returns active people in board order.
func (s *Service) People(ctx context.Context, householdID int64) ([]dbq.Person, error) {
	return s.st.Q.ListPeople(ctx, householdID)
}

// ArchivedPeople returns archived people.
func (s *Service) ArchivedPeople(ctx context.Context, householdID int64) ([]dbq.Person, error) {
	return s.st.Q.ListArchivedPeople(ctx, householdID)
}

// Person returns one person (archived or not).
func (s *Service) Person(ctx context.Context, householdID, id int64) (dbq.Person, error) {
	p, err := s.st.Q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id})
	return p, notFound(err)
}

// CreatePerson adds a family member.
func (s *Service) CreatePerson(ctx context.Context, householdID, actorID int64, in PersonInput) (dbq.Person, error) {
	if err := validatePerson(in).Err(); err != nil {
		return dbq.Person{}, err
	}
	var p dbq.Person
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		if p, err = createPerson(ctx, q, householdID, in, now); err != nil {
			return err
		}
		return log(ctx, q, householdID, actorID, "person.created", "person", p.ID, now)
	})
	return p, err
}

// UpdatePerson edits a family member. Changing an adult to a child removes
// their PIN and sign-in (and fails with ErrLastAdmin if they are the last
// adult who can sign in). Changing a child to an adult removes their PIN, so
// a child's money PIN never starts unlocking admin.
func (s *Service) UpdatePerson(ctx context.Context, householdID, actorID, id int64, in PersonInput) (dbq.Person, error) {
	if err := validatePerson(in).Err(); err != nil {
		return dbq.Person{}, err
	}
	var p dbq.Person
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		cur, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if in.Kind == KindChild && cur.Kind == KindAdult && hasCredential(cur) {
			if err := ensureNotLastAdmin(ctx, q, cur); err != nil {
				return err
			}
			if err := stripCredentials(ctx, q, cur); err != nil {
				return err
			}
		}
		if in.Kind == KindAdult && cur.Kind == KindChild && cur.PinHash != nil {
			if err := q.ClearPersonPIN(ctx, dbq.ClearPersonPINParams{HouseholdID: householdID, ID: id}); err != nil {
				return fmt.Errorf("clear pin: %w", err)
			}
		}
		p, err = q.UpdatePerson(ctx, dbq.UpdatePersonParams{
			Name: strings.TrimSpace(in.Name), Kind: in.Kind, Colour: strings.ToUpper(in.Colour),
			Avatar: in.Avatar, SortOrder: in.SortOrder, PictureCards: pictureCards(in), HouseholdID: householdID, ID: id,
		})
		if err != nil {
			return uniqueErr(err)
		}
		return log(ctx, q, householdID, actorID, "person.updated", "person", id, now)
	})
	return p, err
}

// ArchivePerson hides a person from the board, keeping their history. It
// fails while they have assigned chores, or if they are the last adult who
// can sign in. They're taken out of any shared chores; one left with a
// single person becomes that person's assigned chore.
func (s *Service) ArchivePerson(ctx context.Context, householdID, actorID, id int64) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		p, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if p.ArchivedAt != nil {
			return nil
		}
		n, err := q.CountActiveAssignedChores(ctx, dbq.CountActiveAssignedChoresParams{HouseholdID: householdID, AssigneeID: &id})
		if err != nil {
			return fmt.Errorf("count chores: %w", err)
		}
		if n > 0 {
			return ErrHasAssignedChores
		}
		if hasCredential(p) {
			if err := ensureNotLastAdmin(ctx, q, p); err != nil {
				return err
			}
		}
		at := now.Unix()
		if err := leaveSharedChores(ctx, q, householdID, id, at); err != nil {
			return err
		}
		if err := q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: &at, HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("archive person: %w", err)
		}
		if err := q.DeleteSessionsForPerson(ctx, dbq.DeleteSessionsForPersonParams{HouseholdID: householdID, PersonID: id}); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
		return log(ctx, q, householdID, actorID, "person.archived", "person", id, now)
	})
}

// RestorePerson un-archives a person.
func (s *Service) RestorePerson(ctx context.Context, householdID, actorID, id int64) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		if err := q.UnarchivePerson(ctx, dbq.UnarchivePersonParams{HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("restore person: %w", err)
		}
		return log(ctx, q, householdID, actorID, "person.restored", "person", id, now)
	})
}

// SetPIN sets a person's display PIN. An adult's unlocks admin and opens
// their My money; a child's only opens their own My money. Their other
// sessions end, except keep (the caller's own session, if they are changing
// their own PIN).
func (s *Service) SetPIN(ctx context.Context, householdID, actorID, id int64, pin string, keep []byte) error {
	if err := auth.ValidatePIN(pin); err != nil {
		return valid.Errors{"pin": "Use 4 to 6 digits."}
	}
	hash, err := auth.HashPIN(pin)
	if err != nil {
		return err
	}
	return s.credentialChange(ctx, householdID, actorID, id, "person.pin_set", true, keep, func(q *dbq.Queries, p dbq.Person) error {
		return q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &hash, PinLength: auth.PINLength(pin), HouseholdID: householdID, ID: id})
	})
}

// ClearPIN removes a person's PIN, so they can no longer unlock admin (an
// adult) or open My money on a display.
func (s *Service) ClearPIN(ctx context.Context, householdID, actorID, id int64, keep []byte) error {
	return s.credentialChange(ctx, householdID, actorID, id, "person.pin_cleared", true, keep, func(q *dbq.Queries, p dbq.Person) error {
		if p.Kind == KindAdult && p.PasswordHash == nil && p.PinHash != nil {
			if err := ensureNotLastAdmin(ctx, q, p); err != nil {
				return err
			}
		}
		return q.ClearPersonPIN(ctx, dbq.ClearPersonPINParams{HouseholdID: householdID, ID: id})
	})
}

// SetLogin sets an adult's sign-in email and, if password is non-empty, their
// password. A password is required if they don't have one yet.
func (s *Service) SetLogin(ctx context.Context, householdID, actorID, id int64, email, password string, keep []byte) error {
	errs := valid.Errors{}
	addr, msg := normaliseEmail(email)
	if msg != "" {
		errs.Add("email", msg)
	}
	var hash *string
	if password != "" {
		if err := auth.ValidatePassword(password); err != nil {
			errs.Add("password", "Use 10 to 128 characters.")
		} else {
			h, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			hash = &h
		}
	}
	if err := errs.Err(); err != nil {
		return err
	}
	return s.credentialChange(ctx, householdID, actorID, id, "person.login_set", false, keep, func(q *dbq.Queries, p dbq.Person) error {
		if hash == nil {
			if p.PasswordHash == nil {
				return valid.Errors{"password": "Choose a password (10 to 128 characters)."}
			}
			return uniqueErr(q.SetPersonEmail(ctx, dbq.SetPersonEmailParams{Email: &addr, HouseholdID: householdID, ID: id}))
		}
		return uniqueErr(q.SetPersonLogin(ctx, dbq.SetPersonLoginParams{Email: &addr, PasswordHash: hash, HouseholdID: householdID, ID: id}))
	})
}

// ClearLogin removes an adult's email and password, so they can only use
// their PIN on a household display.
func (s *Service) ClearLogin(ctx context.Context, householdID, actorID, id int64, keep []byte) error {
	return s.credentialChange(ctx, householdID, actorID, id, "person.login_cleared", false, keep, func(q *dbq.Queries, p dbq.Person) error {
		if p.PinHash == nil && p.PasswordHash != nil {
			if err := ensureNotLastAdmin(ctx, q, p); err != nil {
				return err
			}
		}
		return q.ClearPersonLogin(ctx, dbq.ClearPersonLoginParams{HouseholdID: householdID, ID: id})
	})
}

func (s *Service) credentialChange(ctx context.Context, householdID, actorID, id int64, verb string, childOK bool, keep []byte, fn func(*dbq.Queries, dbq.Person) error) error {
	now := s.now()
	if keep == nil {
		keep = []byte{}
	}
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		p, err := q.GetPerson(ctx, dbq.GetPersonParams{HouseholdID: householdID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if p.ArchivedAt != nil {
			return ErrNotFound
		}
		if p.Kind != KindAdult && !childOK {
			return ErrNotAdult
		}
		if err := fn(q, p); err != nil {
			return err
		}
		if err := q.DeleteOtherSessionsForPerson(ctx, dbq.DeleteOtherSessionsForPersonParams{
			HouseholdID: householdID, PersonID: id, KeepTokenHash: keep,
		}); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
		return log(ctx, q, householdID, actorID, verb, "person", id, now)
	})
}

// Rewards returns active rewards.
func (s *Service) Rewards(ctx context.Context, householdID int64) ([]dbq.Reward, error) {
	return s.st.Q.ListRewards(ctx, householdID)
}

// ArchivedRewards returns archived rewards.
func (s *Service) ArchivedRewards(ctx context.Context, householdID int64) ([]dbq.Reward, error) {
	return s.st.Q.ListArchivedRewards(ctx, householdID)
}

// Reward returns one reward.
func (s *Service) Reward(ctx context.Context, householdID, id int64) (dbq.Reward, error) {
	r, err := s.st.Q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: id})
	return r, notFound(err)
}

// CreateReward adds a reward.
func (s *Service) CreateReward(ctx context.Context, householdID, actorID int64, in RewardInput) (dbq.Reward, error) {
	if err := validateReward(in).Err(); err != nil {
		return dbq.Reward{}, err
	}
	var r dbq.Reward
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		r, err = q.CreateReward(ctx, dbq.CreateRewardParams{
			HouseholdID: householdID, Title: strings.TrimSpace(in.Title), Emoji: strings.TrimSpace(in.Emoji), Description: strings.TrimSpace(in.Description),
			CostPoints: in.CostPoints, CostPence: in.CostPence, CreatedAt: now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create reward: %w", err)
		}
		return log(ctx, q, householdID, actorID, "reward.created", "reward", r.ID, now)
	})
	return r, err
}

// UpdateReward edits a reward. Past redemptions keep the cost they had.
func (s *Service) UpdateReward(ctx context.Context, householdID, actorID, id int64, in RewardInput) (dbq.Reward, error) {
	if err := validateReward(in).Err(); err != nil {
		return dbq.Reward{}, err
	}
	var r dbq.Reward
	now := s.now()
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		r, err = q.UpdateReward(ctx, dbq.UpdateRewardParams{
			Title: strings.TrimSpace(in.Title), Emoji: strings.TrimSpace(in.Emoji), Description: strings.TrimSpace(in.Description),
			CostPoints: in.CostPoints, CostPence: in.CostPence, HouseholdID: householdID, ID: id,
		})
		if err != nil {
			return notFound(err)
		}
		return log(ctx, q, householdID, actorID, "reward.updated", "reward", id, now)
	})
	return r, err
}

// ArchiveReward hides a reward. Anyone saving for it loses the goal; it
// doesn't come back if the reward is restored.
func (s *Service) ArchiveReward(ctx context.Context, householdID, actorID, id int64) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		at := now.Unix()
		if err := q.ArchiveReward(ctx, dbq.ArchiveRewardParams{ArchivedAt: &at, HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("archive reward: %w", err)
		}
		if err := q.ClearGoalsForReward(ctx, dbq.ClearGoalsForRewardParams{HouseholdID: householdID, GoalRewardID: &id}); err != nil {
			return fmt.Errorf("clear goals: %w", err)
		}
		return log(ctx, q, householdID, actorID, "reward.archived", "reward", id, now)
	})
}

// RestoreReward un-archives a reward.
func (s *Service) RestoreReward(ctx context.Context, householdID, actorID, id int64) error {
	now := s.now()
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetReward(ctx, dbq.GetRewardParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		if err := q.UnarchiveReward(ctx, dbq.UnarchiveRewardParams{HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("restore reward: %w", err)
		}
		return log(ctx, q, householdID, actorID, "reward.restored", "reward", id, now)
	})
}

func createPerson(ctx context.Context, q *dbq.Queries, householdID int64, in PersonInput, now time.Time) (dbq.Person, error) {
	p, err := q.CreatePerson(ctx, dbq.CreatePersonParams{
		HouseholdID: householdID, Name: strings.TrimSpace(in.Name), Kind: in.Kind,
		Colour: strings.ToUpper(in.Colour), Avatar: in.Avatar, SortOrder: in.SortOrder, PictureCards: pictureCards(in), CreatedAt: now.Unix(),
	})
	if err != nil {
		return dbq.Person{}, uniqueErr(err)
	}
	return p, nil
}

// pictureCards is the stored picture_cards flag: only children have them.
func pictureCards(in PersonInput) int64 {
	if in.PictureCards && in.Kind == KindChild {
		return 1
	}
	return 0
}

func validatePerson(in PersonInput) valid.Errors {
	errs := valid.Errors{}
	if n := len([]rune(strings.TrimSpace(in.Name))); n < 1 || n > 50 {
		errs.Add("name", "Enter a name (up to 50 characters).")
	}
	if in.Kind != KindAdult && in.Kind != KindChild {
		errs.Add("kind", "Choose adult or child.")
	}
	if !colourRe.MatchString(in.Colour) {
		errs.Add("colour", "Choose a colour.")
	}
	if in.SortOrder < 0 || in.SortOrder > 999 {
		errs.Add("sort_order", "Use a number from 0 to 999.")
	}
	return errs
}

func validateReward(in RewardInput) valid.Errors {
	errs := valid.Errors{}
	if n := len([]rune(strings.TrimSpace(in.Title))); n < 1 || n > 100 {
		errs.Add("title", "Enter a title (up to 100 characters).")
	}
	if e := strings.TrimSpace(in.Emoji); e != "" && !emoji.Valid(e) {
		errs.Add("emoji", "Use a single emoji.")
	}
	if len(in.Description) > 1000 {
		errs.Add("description", "Keep it under 1,000 characters.")
	}
	if in.CostPoints < 0 {
		errs.Add("cost_points", "Points can't be negative.")
	}
	if in.CostPence < 0 {
		errs.Add("cost_pence", "Money can't be negative.")
	}
	if in.CostPoints == 0 && in.CostPence == 0 {
		errs.Add("cost_points", "Set a cost in points, money or both.")
	}
	return errs
}

func normaliseEmail(s string) (addr, msg string) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 254 {
		return "", "Enter an email address like name@example.com."
	}
	return strings.ToLower(s), ""
}

func hasCredential(p dbq.Person) bool { return p.PinHash != nil || p.PasswordHash != nil }

func ensureNotLastAdmin(ctx context.Context, q *dbq.Queries, p dbq.Person) error {
	n, err := q.CountAdultsWithCredential(ctx, p.HouseholdID)
	if err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

func stripCredentials(ctx context.Context, q *dbq.Queries, p dbq.Person) error {
	if err := q.ClearPersonPIN(ctx, dbq.ClearPersonPINParams{HouseholdID: p.HouseholdID, ID: p.ID}); err != nil {
		return fmt.Errorf("clear pin: %w", err)
	}
	if err := q.ClearPersonLogin(ctx, dbq.ClearPersonLoginParams{HouseholdID: p.HouseholdID, ID: p.ID}); err != nil {
		return fmt.Errorf("clear login: %w", err)
	}
	if err := q.DeleteSessionsForPerson(ctx, dbq.DeleteSessionsForPersonParams{HouseholdID: p.HouseholdID, PersonID: p.ID}); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	return nil
}

func log(ctx context.Context, q *dbq.Queries, householdID, actorID int64, verb, subjectType string, subjectID int64, now time.Time) error {
	return store.LogActivity(ctx, q, store.Activity{
		HouseholdID: householdID, ActorID: &actorID, Verb: verb,
		SubjectType: subjectType, SubjectID: subjectID, At: now,
	})
}

// uniqueErr turns UNIQUE violations into field errors.
func uniqueErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case store.IsUnique(err, "people", "name"):
		return valid.Errors{"name": "Someone in the household already has that name."}
	case store.IsUnique(err, "people", "email"):
		return valid.Errors{"email": "That email is already in use."}
	}
	return err
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// leaveSharedChores takes personID out of every shared chore. A shared chore
// down to one person becomes theirs: assigned, with its not-yet-started
// occurrences given to them.
func leaveSharedChores(ctx context.Context, q *dbq.Queries, householdID, personID, at int64) error {
	if err := q.RemovePersonFromSharedChores(ctx, dbq.RemovePersonFromSharedChoresParams{HouseholdID: householdID, PersonID: personID}); err != nil {
		return fmt.Errorf("leave shared chores: %w", err)
	}
	// Their copies of "each" chores still to do go too.
	if err := q.DeleteOpenEachOccurrencesForPerson(ctx, dbq.DeleteOpenEachOccurrencesForPersonParams{HouseholdID: householdID, PersonID: personID}); err != nil {
		return fmt.Errorf("leave each chores: %w", err)
	}
	solo, err := q.ListSharedChoresWithOnePerson(ctx, householdID)
	if err != nil {
		return fmt.Errorf("shared chores: %w", err)
	}
	for _, c := range solo {
		if err := q.MakeChoreAssigned(ctx, dbq.MakeChoreAssignedParams{
			PersonID: &c.PersonID, UpdatedAt: at, HouseholdID: householdID, ID: c.ChoreID,
		}); err != nil {
			return fmt.Errorf("assign chore: %w", err)
		}
		if err := q.AssignOpenOccurrences(ctx, dbq.AssignOpenOccurrencesParams{
			PersonID: &c.PersonID, HouseholdID: householdID, ChoreID: c.ChoreID,
		}); err != nil {
			return fmt.Errorf("assign occurrences: %w", err)
		}
		if err := q.DeleteChorePeople(ctx, dbq.DeleteChorePeopleParams{HouseholdID: householdID, ChoreID: c.ChoreID}); err != nil {
			return fmt.Errorf("clear chore people: %w", err)
		}
	}
	return nil
}
