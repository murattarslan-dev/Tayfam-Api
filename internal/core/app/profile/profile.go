// Package profile shows another user's profile (issue #71; data model v14
// "Profil görünürlüğü", "Kalıcı katılım geçmişi"): name and avatar, the
// tayfas the viewer shares with the user, and the attendance history as far
// as the user's activity_visibility lets the viewer see it.
//
// The visibility table (v14) is Access; the viewer's audience is decided
// once per request:
//
//	setting  | friends            | shared tayfa         | everyone else
//	private  | nothing            | nothing              | nothing
//	tayfa    | all, no delay      | without last 14 days | nothing
//	public   | all, no delay      | without last 14 days | without last 14 days
//
// The user always sees all of their own history. A record's tayfa name, its
// event title, duration and IDs are shown only to active members of that
// tayfa; everyone else sees the type, mode, date and city/district, so a
// private tayfa does not leak through a profile.
//
// A block in either direction hides the whole profile: the user looks
// unknown (apperr.ErrUserNotFound), exactly as a user who does not exist.
package profile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Delay is how far back the latest records are hidden from viewers who
// are not friends (v14: "son 14 gün").
const Delay = 14 * 24 * time.Hour

// MaxMutualTayfas bounds the shared tayfa list of a profile.
const MaxMutualTayfas = 100

// Paging: page_size 0 means 50, above 100 means 100.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// Visibility is user_profiles.activity_visibility.
type Visibility string

// Visibility values.
const (
	VisibilityPrivate Visibility = "private"
	VisibilityTayfa   Visibility = "tayfa"
	VisibilityPublic  Visibility = "public"
)

// Audience is who the viewer is to the user, the strongest that applies.
type Audience int

// Audiences.
const (
	AudienceOther Audience = iota
	AudienceMutualTayfa
	AudienceFriend
	AudienceSelf
)

// Access is the visibility table: whether a viewer of audience a sees the
// history of a user with setting v, and whether the last Delay of it is
// hidden. An unknown setting shows nothing.
func Access(v Visibility, a Audience) (visible, delayed bool) {
	if a == AudienceSelf {
		return true, false
	}
	switch v {
	case VisibilityTayfa:
		switch a {
		case AudienceFriend:
			return true, false
		case AudienceMutualTayfa:
			return true, true
		}
	case VisibilityPublic:
		if a == AudienceFriend {
			return true, false
		}
		return true, true
	}
	return false, false
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time // optional, for tests
}

// Service shows profiles.
type Service struct {
	q   *store.Queries
	now func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("profile: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{q: store.New(cfg.Pool), now: func() time.Time { return cfg.Now().UTC() }}, nil
}

// Tayfa is a shared tayfa.
type Tayfa struct {
	ID   id.ID
	Name string
}

// Profile is a user as the viewer may see them.
type Profile struct {
	UserID         id.ID
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
	Self           bool
	// Friend is the friendship as the viewer sees it; "" when none.
	Friend social.State
	// MutualTayfas are live tayfas both are active members of; empty for
	// one's own profile.
	MutualTayfas []Tayfa
	// ActivityVisible tells whether the viewer may list the history at all.
	ActivityVisible bool
	// ActivityVisibility is filled only on one's own profile.
	ActivityVisibility Visibility
}

// Activity is one attendance record as the viewer may see it. Fields after
// StartedAt are filled only when Member is true.
type Activity struct {
	EventType string
	Mode      string
	City      string
	District  string
	StartedAt time.Time
	// Member: the viewer is an active member of the record's tayfa.
	Member          bool
	TayfaID         id.ID
	TayfaName       string
	EventID         id.ID
	EventTitle      string
	DurationMinutes int32
}

// Page is one page of a list.
type Page[T any] struct {
	Items []T
	// Next is the token of the next page; empty on the last page.
	Next string
}

// GetProfile returns subject's profile as caller sees it.
func (s *Service) GetProfile(ctx context.Context, caller, subject id.ID) (Profile, error) {
	v, err := s.view(ctx, caller, subject)
	if err != nil {
		return Profile{}, err
	}
	out := Profile{UserID: subject, DisplayName: v.p.DisplayName.String, Self: v.audience == AudienceSelf, Friend: v.friend}
	if v.p.AvatarUploadID.Valid {
		out.AvatarUploadID = id.ID(v.p.AvatarUploadID.Bytes)
	}
	out.ActivityVisible, _ = Access(Visibility(v.p.ActivityVisibility), v.audience)
	if out.Self {
		out.ActivityVisibility = Visibility(v.p.ActivityVisibility)
		return out, nil
	}
	rows, err := s.q.ListMutualTayfas(ctx, store.ListMutualTayfasParams{ViewerID: caller.PG(), SubjectID: subject.PG(), MaxCount: MaxMutualTayfas})
	if err != nil {
		return Profile{}, fmt.Errorf("ortak tayfalar: %w", err)
	}
	out.MutualTayfas = make([]Tayfa, 0, len(rows))
	for _, r := range rows {
		out.MutualTayfas = append(out.MutualTayfas, Tayfa{ID: id.ID(r.ID.Bytes), Name: r.Name})
	}
	return out, nil
}

// ListActivity returns subject's attendance history as caller may see it,
// newest first. A viewer the setting shows nothing to gets an empty list,
// not an error: the profile itself is visible.
func (s *Service) ListActivity(ctx context.Context, caller, subject id.ID, size int32, token string) (Page[Activity], error) {
	size, err := pageSize(size)
	if err != nil {
		return Page[Activity]{}, err
	}
	c, ok, err := decodeCursor(token, subject)
	if err != nil {
		return Page[Activity]{}, err
	}
	v, err := s.view(ctx, caller, subject)
	if err != nil {
		return Page[Activity]{}, err
	}
	visible, delayed := Access(Visibility(v.p.ActivityVisibility), v.audience)
	if !visible {
		return Page[Activity]{Items: []Activity{}}, nil
	}
	now := s.now()
	notAfter := now.Add(time.Hour) // records are written at check-in, never ahead
	if delayed {
		notAfter = now.Add(-Delay)
	}
	p := store.ListActivityPageParams{
		ViewerID: caller.PG(), UserID: subject.PG(), NotAfter: ts(notAfter), PageSize: size + 1,
		BeforeTime: ts(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)), BeforeID: maxID().PG(),
	}
	if ok {
		p.BeforeTime, p.BeforeID = ts(time.UnixMicro(c.Micros).UTC()), c.id.PG()
	}
	rows, err := s.q.ListActivityPage(ctx, p)
	if err != nil {
		return Page[Activity]{}, fmt.Errorf("katılım geçmişi: %w", err)
	}
	out := Page[Activity]{Items: make([]Activity, 0, len(rows))}
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			out.Next = encodeCursor(subject, last.StartedAt.Time, id.ID(last.ID.Bytes))
			break
		}
		a := Activity{EventType: r.EventType, Mode: r.Mode, City: r.City.String, District: r.District.String, StartedAt: r.StartedAt.Time.UTC()}
		if r.ViewerMember {
			a.Member = true
			a.TayfaID = id.ID(r.TayfaID.Bytes)
			a.TayfaName = r.TayfaName
			a.EventTitle = r.EventTitle
			a.DurationMinutes = r.DurationMinutes
			if r.EventID.Valid {
				a.EventID = id.ID(r.EventID.Bytes)
			}
		}
		out.Items = append(out.Items, a)
	}
	return out, nil
}

type view struct {
	p        store.GetLiveUserProfileRow
	audience Audience
	friend   social.State
}

// view loads subject for caller: unknown, deleted and blocked users are
// all ErrUserNotFound.
func (s *Service) view(ctx context.Context, caller, subject id.ID) (view, error) {
	if subject == (id.ID{}) {
		return view{}, apperr.Validation("user_id")
	}
	p, err := s.q.GetLiveUserProfile(ctx, subject.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return view{}, apperr.ErrUserNotFound
	}
	if err != nil {
		return view{}, fmt.Errorf("profil: %w", err)
	}
	if caller == subject {
		return view{p: p, audience: AudienceSelf}, nil
	}
	blocked, err := social.IsBlocked(ctx, s.q, caller, subject)
	if err != nil {
		return view{}, err
	}
	if blocked {
		return view{}, apperr.ErrUserNotFound
	}
	rel, err := social.Relation(ctx, s.q, caller, subject)
	if err != nil {
		return view{}, err
	}
	out := view{p: p, audience: AudienceOther, friend: rel}
	if rel == social.StateFriends {
		out.audience = AudienceFriend
		return out, nil
	}
	mutual, err := s.q.HasMutualTayfa(ctx, store.HasMutualTayfaParams{ViewerID: caller.PG(), SubjectID: subject.PG()})
	if err != nil {
		return view{}, fmt.Errorf("ortak tayfa: %w", err)
	}
	if mutual {
		out.audience = AudienceMutualTayfa
	}
	return out, nil
}

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// cursor is the position after the last record of a page. It names the
// profile it belongs to; it carries nothing the viewer could not see.
type cursor struct {
	User   string `json:"u"`
	Micros int64  `json:"c"`
	ID     string `json:"i"`
	id     id.ID
}

func encodeCursor(subject id.ID, at time.Time, last id.ID) string {
	b, _ := json.Marshal(cursor{User: subject.String(), Micros: at.UnixMicro(), ID: last.String()}) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(token string, subject id.ID) (cursor, bool, error) {
	if token == "" {
		return cursor{}, false, nil
	}
	bad := apperr.Validation("page_token")
	if len(token) > 512 {
		return cursor{}, false, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil || c.User != subject.String() {
		return cursor{}, false, bad
	}
	u, ok := id.Parse(c.ID)
	if !ok {
		return cursor{}, false, bad
	}
	c.id = u
	return c, true, nil
}

func maxID() id.ID {
	var u id.ID
	for i := range u {
		u[i] = 0xff
	}
	return u
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
