// Package profile shows another user the profile details identity owns:
// the birthday (issue #185; Notion Proje Planı §4 item 8, ADR-0012) and
// the profile completion fields (issue #186).
//
// Who sees what depends on the relationship between the viewer and the
// profile's user. identity does not keep friendships, tayfas or blocks, and
// does not take the relationship from the request either (ADR-0011): it
// asks core with the viewer's own token (Relations). Values are read only
// when shown, never cached and never logged.
package profile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profilefields"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var (
	// ErrUserNotFound: no registered, live user, or a block between the
	// two in either direction (core's answer); the same error for both.
	ErrUserNotFound = errors.New("kullanıcı bulunamadı")
	// ErrUnavailable: core could not be asked.
	ErrUnavailable = errors.New("ilişki sorulamadı")
)

// Relation is how the viewer stands to the profile's user, as core sees it.
type Relation struct {
	Self   bool
	Friend bool // an accepted friendship, not a request
	Mutual bool // both are active members of at least one live tayfa
}

// Relations asks core about the viewer (the verified caller of ctx, whose
// token the call carries) and user. A block in either direction, or a user
// core does not know, is ErrUserNotFound; core unreachable is
// ErrUnavailable.
type Relations interface {
	Relation(ctx context.Context, user id.ID) (Relation, error)
}

// Config wires a Service.
type Config struct {
	Pool      *pgxpool.Pool
	Cipher    *birthdate.Cipher
	Relations Relations
	Now       func() time.Time // optional, for tests
}

// Service implements the profile details.
type Service struct {
	q   *store.Queries
	c   *birthdate.Cipher
	rel Relations
	now func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Cipher == nil || cfg.Relations == nil {
		return nil, errors.New("profile: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{q: store.New(cfg.Pool), c: cfg.Cipher, rel: cfg.Relations, now: cfg.Now}, nil
}

// Birthday is the part of the birth date shown. Year and Age are zero
// unless the user shows the full date.
type Birthday struct {
	Month time.Month
	Day   int
	Year  int
	Age   int
}

// About is the profile completion fields shown (issue #186). Declined
// fields look the same as empty ones: nobody else learns which was
// declined.
type About struct {
	Interests      []string
	About          string
	Occupation     string
	Team           string
	Languages      []string
	SocialAccounts []profilefields.SocialAccount
}

// Details is what the viewer may see; nil fields are not shown.
type Details struct {
	Birthday *Birthday
	About    *About
}

// Get returns what viewer may see of user's details. The relationship is
// asked of core first, so a blocked or unknown user costs no decryption.
func (s *Service) Get(ctx context.Context, viewer, user id.ID) (Details, error) {
	rel, err := s.rel.Relation(ctx, user)
	if err != nil {
		return Details{}, err
	}
	// core answers for the token's subject; a token of another user than
	// viewer would be a bug in the layers above.
	if rel.Self != (viewer == user) {
		return Details{}, fmt.Errorf("profil: core'un ilişkisi çağıranla uyuşmuyor")
	}
	u, err := s.q.GetProfileDetails(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return Details{}, ErrUserNotFound
	}
	if err != nil {
		return Details{}, fmt.Errorf("profil: %w", err)
	}
	b, err := s.birthday(user, rel, u)
	if err != nil {
		return Details{}, err
	}
	about, err := s.about(ctx, user, rel)
	if err != nil {
		return Details{}, err
	}
	return Details{Birthday: b, About: about}, nil
}

// birthday applies the setting: only friends see it, as much as the user
// chose; the user always sees their own full date (ADR-0012 "Görünürlük").
// The audience (users.birth_date_audience) is not read: it is "friends"
// for every account and no endpoint changes it (issue #185).
func (s *Service) birthday(user id.ID, rel Relation, u store.GetProfileDetailsRow) (*Birthday, error) {
	vis := account.BirthdayVisibility(u.BirthDateVisibility)
	if !rel.Self && (!rel.Friend || vis == account.BirthdayHidden || !vis.Valid()) {
		return nil, nil
	}
	date, err := s.c.Decrypt(user, u.BirthDateEnc)
	if err != nil {
		return nil, fmt.Errorf("doğum tarihi: %w", err)
	}
	b := &Birthday{Month: date.Month, Day: date.Day}
	if rel.Self || vis == account.BirthdayFull {
		b.Year, b.Age = date.Year, date.Age(s.now())
	}
	return b, nil
}

// about applies the about visibility (issue #186):
//
//	setting             | friend          | shared tayfa only    | anyone else
//	friends_and_mutual  | all             | all but social       | nothing
//	friends             | all             | nothing              | nothing
//	none                | nothing         | nothing              | nothing
//
// The user always sees all of their own. Social accounts open a new way to
// reach the user, so they go to friends at most.
func (s *Service) about(ctx context.Context, user id.ID, rel Relation) (*About, error) {
	st, err := profilefields.Load(ctx, s.q, user)
	if err != nil {
		return nil, err
	}
	social := rel.Self
	switch {
	case rel.Self:
	case rel.Friend && (st.Visibility == profilefields.VisibleFriendsAndMutual || st.Visibility == profilefields.VisibleFriends):
		social = true
	case rel.Mutual && st.Visibility == profilefields.VisibleFriendsAndMutual:
	default:
		return nil, nil
	}
	v := st.Values
	a := &About{Interests: v.Interests, About: v.About, Occupation: v.Occupation, Team: v.Team, Languages: v.Languages}
	if social {
		a.SocialAccounts = v.SocialAccounts
	}
	if len(a.Interests) == 0 && a.About == "" && a.Occupation == "" && a.Team == "" && len(a.Languages) == 0 && len(a.SocialAccounts) == 0 {
		return nil, nil
	}
	return a, nil
}
