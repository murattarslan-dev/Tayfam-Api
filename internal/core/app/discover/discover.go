// Package discover is the first stage of Keşfet: events of tayfas opened
// to Keşfet that the caller's friends go to, the tayfa admins' setting, and
// the safety net around it (issue #210, ADR-0033).
//
// The rules that must never drift live in the database (migration 00028):
// tayfa_discover_listable (which tayfa can be listed), discover_friend_visible
// (which friend's attendance the viewer may see) and the triggers that take
// a tayfa out of Keşfet. This package adds the caller checks: the feature
// flag, the permission table and the event's state.
//
// Feature flag: with Config.Enabled false (DISCOVER_ENABLED, default off)
// the feed, the feedback and the safety contact methods return
// apperr.ErrFeatureDisabled before reading anything. The tayfa setting
// works regardless.
//
// Nothing here stores, logs or returns a device location. The friendship
// score orders the feed inside the query and never leaves it.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Product limits (ADR-0033). They are code, not configuration: a changed
// environment must not widen what Keşfet shows (ADR-0011).
const (
	// FeedSize is the most items the feed returns; there is no paging.
	FeedSize = 50
	// MinMembers is the fewest active members a listed tayfa has.
	MinMembers = 5
	// FeedbackWindow is how long after the end feedback is taken.
	FeedbackWindow = 7 * 24 * time.Hour
	// FrozenAfter is when an event's numbers stop changing (ADR-0026 §1).
	FrozenAfter = 7 * 24 * time.Hour
	// Safety contact grace time bounds and default, in minutes.
	MinGrace     = 15
	MaxGrace     = 1440
	DefaultGrace = 60
	// A share stays on the contact's list this long after the event's end,
	// or this long after its alert.
	ShareAfterEnd   = 24 * time.Hour
	ShareAfterAlert = 7 * 24 * time.Hour
)

// Tayfa states as the tayfa's admins see them (TayfaDiscoverState).
const (
	StateOff         = "off"
	StatePending     = "pending"
	StateListed      = "listed"
	StateUnderReview = "under_review"
	StateRejected    = "rejected"
)

// Feedback answers (event_feedback.answer).
const (
	AnswerOK      = "ok"
	AnswerNotOK   = "not_ok"
	AnswerSerious = "serious"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Enabled is DISCOVER_ENABLED.
	Enabled bool
	Now     func() time.Time // optional, for tests
}

// Service implements DiscoverService except JoinFromDiscover (package
// membership, which owns joining).
type Service struct {
	pool    *pgxpool.Pool
	enabled bool
	now     func() time.Time
	q       *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("discover: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		pool: cfg.Pool, enabled: cfg.Enabled, q: store.New(cfg.Pool),
		now: func() time.Time { return cfg.Now().UTC().Truncate(time.Microsecond) },
	}, nil
}

func (s *Service) gate() error {
	if !s.enabled {
		return apperr.ErrFeatureDisabled
	}
	return nil
}

// ---------------------------------------------------------------------------
// Feed

// Person is a friend on a feed item or a safety contact.
type Person struct {
	ID          id.ID
	DisplayName string
	AvatarID    id.ID // zero when none
}

// Item is one feed entry. It never carries the score.
type Item struct {
	EventID        id.ID
	Type           string
	Title          string
	StartsAt       time.Time
	EndsAt         time.Time
	City, District string

	TayfaID          id.ID
	TayfaName        string
	TayfaDescription string
	TayfaCity        string
	TayfaCreatedAt   time.Time
	EventCount       int32
	AttendanceCount  int32

	Friends     []Person // at most three
	FriendCount int32
}

// Feed lists the caller's Keşfet feed, best first (at most FeedSize).
func (s *Service) Feed(ctx context.Context, viewer id.ID) ([]Item, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	now := s.now()
	rows, err := s.q.ListDiscoverFeed(ctx, store.ListDiscoverFeedParams{
		Viewer: viewer.PG(), Now: ts(now), FrozenBefore: ts(now.Add(-FrozenAfter)), N: FeedSize,
	})
	if err != nil {
		return nil, fmt.Errorf("keşfet akışı: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		var fr []struct {
			UserID      string  `json:"user_id"`
			DisplayName *string `json:"display_name"`
			Avatar      *string `json:"avatar_upload_id"`
		}
		if err := json.Unmarshal(r.Friends, &fr); err != nil {
			return nil, fmt.Errorf("keşfet arkadaşları: %w", err)
		}
		it := Item{
			EventID: idOf(r.EventID), Type: r.Type, Title: r.Title, StartsAt: r.StartsAt.Time, EndsAt: r.EndsAt.Time,
			City: r.EventCity.String, District: r.EventDistrict.String,
			TayfaID: idOf(r.TayfaID), TayfaName: r.TayfaName, TayfaDescription: r.TayfaDescription.String,
			TayfaCity: r.TayfaCity.String, TayfaCreatedAt: r.TayfaCreatedAt.Time,
			EventCount: r.EventCount, AttendanceCount: r.AttendanceCount, FriendCount: r.FriendCount,
		}
		for _, f := range fr {
			u, ok := id.Parse(f.UserID)
			if !ok || f.DisplayName == nil {
				continue
			}
			p := Person{ID: u, DisplayName: *f.DisplayName}
			if f.Avatar != nil {
				p.AvatarID, _ = id.Parse(*f.Avatar)
			}
			it.Friends = append(it.Friends, p)
		}
		out = append(out, it)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The tayfa admins' setting (works with the flag off)

// TayfaState is the tayfa's Keşfet state as its admins see it.
type TayfaState struct {
	State   string // State*
	Enabled bool
}

// stateOf maps the admins' wish and the platform's decision. The reason a
// tayfa was dropped is never told (ADR-0033 §7).
func stateOf(visibility string, review pgtype.Text) TayfaState {
	enabled := visibility == "listed"
	var st string
	switch {
	case review.Valid && review.String == "rejected":
		st = StateRejected
	case review.Valid && review.String == "dropped":
		st = StateUnderReview
	case !enabled:
		st = StateOff
	case review.Valid && review.String == "approved":
		st = StateListed
	default:
		st = StatePending
	}
	return TayfaState{State: st, Enabled: enabled}
}

// KindStandard is tayfas.kind of a tayfa that may be in Keşfet; an
// instant tayfa (ADR-0032) never is (ADR-0033 §12).
const KindStandard = "standard"

// GetTayfa returns the tayfa's Keşfet state. Admins only. An instant
// tayfa has no Keşfet state: it is apperr.ErrTayfaNotFound here.
func (s *Service) GetTayfa(ctx context.Context, caller, tayfa id.ID) (TayfaState, error) {
	t, m, err := access.Load(ctx, s.q, tayfa, caller, false)
	if err != nil {
		return TayfaState{}, err
	}
	if t.Kind != KindStandard {
		return TayfaState{}, apperr.ErrTayfaNotFound
	}
	if err := access.Authorize(m, authz.TayfaDiscoverManage, s.now()); err != nil {
		return TayfaState{}, err
	}
	r, err := s.q.GetTayfaDiscoverState(ctx, tayfa.PG())
	if err != nil {
		return TayfaState{}, fmt.Errorf("keşfet durumu: %w", err)
	}
	return stateOf(r.Visibility, r.ReviewStatus), nil
}

// SetTayfa turns the admins' wish on or off. Turning it on needs at least
// MinMembers active members and puts a new or rejected application in the
// platform's queue; a pending, approved or dropped one keeps its state.
// Turning it off hides the tayfa at once. Admins only; the change and its
// audit record are one transaction.
func (s *Service) SetTayfa(ctx context.Context, caller, tayfa id.ID, enabled bool) (TayfaState, error) {
	var out TayfaState
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, caller, true)
		if err != nil {
			return err
		}
		if t.Kind != KindStandard {
			return apperr.ErrTayfaNotFound // instant tayfas never enter Keşfet
		}
		now := s.now()
		if err := access.Authorize(m, authz.TayfaDiscoverManage, now); err != nil {
			return err
		}
		if access.ReadOnly(t) {
			return apperr.ErrTayfaReadOnly
		}
		visibility := "private"
		if enabled {
			visibility = "listed"
			n, err := q.CountActiveMembers(ctx, tayfa.PG())
			if err != nil {
				return fmt.Errorf("üye sayısı: %w", err)
			}
			if n < MinMembers {
				return apperr.ErrDiscoverTooFewMembers
			}
			if err := q.RequestTayfaDiscover(ctx, store.RequestTayfaDiscoverParams{
				TayfaID: tayfa.PG(), Now: ts(now), RequestedBy: caller.PG(),
			}); err != nil {
				return fmt.Errorf("keşfet başvurusu: %w", err)
			}
		}
		changed, err := q.SetTayfaVisibility(ctx, store.SetTayfaVisibilityParams{ID: tayfa.PG(), Visibility: visibility})
		if err != nil {
			return fmt.Errorf("keşfet ayarı: %w", err)
		}
		if changed == 1 {
			action := "tayfa.discover_disabled"
			if enabled {
				action = "tayfa.discover_enabled"
			}
			if err := txn.Audit(ctx, q, tayfa, caller, action, txn.Target{Type: "tayfa", ID: tayfa}, nil); err != nil {
				return err
			}
		}
		r, err := q.GetTayfaDiscoverState(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("keşfet durumu: %w", err)
		}
		out = stateOf(r.Visibility, r.ReviewStatus)
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// "Was everything all right?"

// Feedback is the caller's own answer; Answer is "" when there is none.
type Feedback struct {
	Answer    string
	UpdatedAt time.Time
}

// ValidAnswer reports whether a is an answer the caller may give.
func ValidAnswer(a string) bool { return a == AnswerOK || a == AnswerNotOK || a == AnswerSerious }

// eventFor loads the caller's active membership, checks action a and the
// event module, and returns the event.
func (s *Service) eventFor(ctx context.Context, q *store.Queries, caller, tayfa, event id.ID, a authz.Action, lock bool) (store.GetEventRow, error) {
	_, m, err := access.Load(ctx, q, tayfa, caller, lock)
	if err != nil {
		return store.GetEventRow{}, err
	}
	if err := access.Authorize(m, a, s.now()); err != nil {
		return store.GetEventRow{}, err
	}
	if err := access.RequireModule(ctx, q, tayfa, access.ModuleEvents); err != nil {
		return store.GetEventRow{}, err
	}
	ev, err := q.GetEvent(ctx, store.GetEventParams{TayfaID: tayfa.PG(), ID: event.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetEventRow{}, apperr.ErrEventNotFound
	}
	if err != nil {
		return store.GetEventRow{}, fmt.Errorf("etkinlik: %w", err)
	}
	return ev, nil
}

// GetFeedback returns the caller's answer for the event.
func (s *Service) GetFeedback(ctx context.Context, caller, tayfa, event id.ID) (Feedback, error) {
	if err := s.gate(); err != nil {
		return Feedback{}, err
	}
	if _, err := s.eventFor(ctx, s.q, caller, tayfa, event, authz.EventFeedback, false); err != nil {
		return Feedback{}, err
	}
	r, err := s.q.GetEventFeedback(ctx, store.GetEventFeedbackParams{EventID: event.PG(), UserID: caller.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Feedback{}, nil
	}
	if err != nil {
		return Feedback{}, fmt.Errorf("geri bildirim: %w", err)
	}
	return Feedback{Answer: r.Answer, UpdatedAt: r.UpdatedAt.Time}, nil
}

// SetFeedback writes the caller's answer: an attendee of a discoverable
// in-person event, from its end until FeedbackWindow after. A serious
// answer takes the tayfa out of Keşfet (trigger event_feedback_discover_drop).
func (s *Service) SetFeedback(ctx context.Context, caller, tayfa, event id.ID, answer string) (Feedback, error) {
	if err := s.gate(); err != nil {
		return Feedback{}, err
	}
	if !ValidAnswer(answer) {
		return Feedback{}, apperr.Validation("answer")
	}
	var out Feedback
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		ev, err := s.eventFor(ctx, q, caller, tayfa, event, authz.EventFeedback, true)
		if err != nil {
			return err
		}
		now := s.now()
		if !ev.Discoverable || ev.Mode != "in_person" || ev.Status == "cancelled" ||
			now.Before(ev.EndsAt.Time) || now.After(ev.EndsAt.Time.Add(FeedbackWindow)) {
			return apperr.ErrFeedbackClosed
		}
		r, err := q.GetRsvpForCheckIn(ctx, store.GetRsvpForCheckInParams{EventID: event.PG(), UserID: caller.PG()})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.Attendance != "attended") {
			return apperr.ErrNotAttended
		}
		if err != nil {
			return fmt.Errorf("katılım: %w", err)
		}
		w, err := q.UpsertEventFeedback(ctx, store.UpsertEventFeedbackParams{EventID: event.PG(), UserID: caller.PG(), Answer: answer})
		if err != nil {
			return fmt.Errorf("geri bildirim: %w", err)
		}
		out = Feedback{Answer: w.Answer, UpdatedAt: w.UpdatedAt.Time}
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Safety contact

// SafetyContact is the caller's own safety contact for an event; Contact
// is zero when none is chosen.
type SafetyContact struct {
	Contact      Person
	GraceMinutes int32
	ReturnedAt   time.Time
	AlertSentAt  time.Time
}

func (s *Service) contactOf(ctx context.Context, q *store.Queries, caller, event id.ID) (SafetyContact, bool, error) {
	r, err := q.GetSafetyContact(ctx, store.GetSafetyContactParams{EventID: event.PG(), UserID: caller.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return SafetyContact{}, false, nil
	}
	if err != nil {
		return SafetyContact{}, false, fmt.Errorf("güvenlik kişisi: %w", err)
	}
	return SafetyContact{
		Contact:      Person{ID: idOf(r.ContactUserID), DisplayName: r.DisplayName.String, AvatarID: idOf(r.AvatarUploadID)},
		GraceMinutes: r.GraceMinutes, ReturnedAt: r.ReturnedAt.Time, AlertSentAt: r.AlertSentAt.Time,
	}, true, nil
}

// GetSafetyContact returns the caller's safety contact for the event.
func (s *Service) GetSafetyContact(ctx context.Context, caller, tayfa, event id.ID) (SafetyContact, error) {
	if err := s.gate(); err != nil {
		return SafetyContact{}, err
	}
	if _, err := s.eventFor(ctx, s.q, caller, tayfa, event, authz.EventSafetyContact, false); err != nil {
		return SafetyContact{}, err
	}
	c, _, err := s.contactOf(ctx, s.q, caller, event)
	return c, err
}

// SetSafetyContact chooses or replaces the caller's safety contact. grace
// 0 means DefaultGrace.
func (s *Service) SetSafetyContact(ctx context.Context, caller, tayfa, event, contact id.ID, grace int32) (SafetyContact, error) {
	if err := s.gate(); err != nil {
		return SafetyContact{}, err
	}
	if grace == 0 {
		grace = DefaultGrace
	}
	if grace < MinGrace || grace > MaxGrace {
		return SafetyContact{}, apperr.Validation("grace_minutes")
	}
	if contact == (id.ID{}) {
		return SafetyContact{}, apperr.Validation("contact_user_id")
	}
	if contact == caller {
		return SafetyContact{}, apperr.ErrNotFriends
	}
	var out SafetyContact
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		ev, err := s.eventFor(ctx, q, caller, tayfa, event, authz.EventSafetyContact, true)
		if err != nil {
			return err
		}
		if ev.Mode != "in_person" || ev.Status == "cancelled" || !s.now().Before(ev.EndsAt.Time) {
			return apperr.ErrSafetyContactClosed
		}
		r, err := q.GetRsvpForCheckIn(ctx, store.GetRsvpForCheckInParams{EventID: event.PG(), UserID: caller.PG()})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.Status != "going") {
			return apperr.ErrEventNotGoing
		}
		if err != nil {
			return fmt.Errorf("katılım: %w", err)
		}
		ok, err := q.SafetyContactAllowed(ctx, store.SafetyContactAllowedParams{UserID: caller.PG(), ContactID: contact.PG()})
		if err != nil {
			return fmt.Errorf("güvenlik kişisi: %w", err)
		}
		if !ok.Valid || !ok.Bool {
			return apperr.ErrNotFriends
		}
		n, err := q.UpsertSafetyContact(ctx, store.UpsertSafetyContactParams{
			EventID: event.PG(), UserID: caller.PG(), ContactUserID: contact.PG(), GraceMinutes: grace,
		})
		if err != nil {
			return fmt.Errorf("güvenlik kişisi: %w", err)
		}
		if n != 1 {
			return apperr.ErrSafetyContactClosed // the alert went out
		}
		out, _, err = s.contactOf(ctx, q, caller, event)
		return err
	})
	return out, err
}

// ClearSafetyContact removes the caller's safety contact; nothing to
// remove succeeds.
func (s *Service) ClearSafetyContact(ctx context.Context, caller, tayfa, event id.ID) error {
	if err := s.gate(); err != nil {
		return err
	}
	return txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := s.eventFor(ctx, q, caller, tayfa, event, authz.EventSafetyContact, true); err != nil {
			return err
		}
		if err := q.DeleteSafetyContact(ctx, store.DeleteSafetyContactParams{EventID: event.PG(), UserID: caller.PG()}); err != nil {
			return fmt.Errorf("güvenlik kişisi: %w", err)
		}
		return nil
	})
}

// MarkReturned records "I'm back"; the first time is kept.
func (s *Service) MarkReturned(ctx context.Context, caller, tayfa, event id.ID) (SafetyContact, error) {
	if err := s.gate(); err != nil {
		return SafetyContact{}, err
	}
	var out SafetyContact
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := s.eventFor(ctx, q, caller, tayfa, event, authz.EventSafetyContact, true); err != nil {
			return err
		}
		n, err := q.MarkSafetyReturned(ctx, store.MarkSafetyReturnedParams{EventID: event.PG(), UserID: caller.PG(), Now: ts(s.now())})
		if err != nil {
			return fmt.Errorf("döndüm: %w", err)
		}
		if n != 1 {
			return apperr.ErrSafetyContactNotFound
		}
		out, _, err = s.contactOf(ctx, q, caller, event)
		return err
	})
	return out, err
}

// Share is an event a participant shared with the caller as their safety
// contact: the meeting point's name only, never coordinates.
type Share struct {
	Participant      Person
	TayfaName        string
	EventID          id.ID
	EventTitle       string
	StartsAt, EndsAt time.Time
	MeetingPointName string
	City, District   string
	GraceMinutes     int32
	ReturnedAt       time.Time
	AlertSentAt      time.Time
}

// ListSafetyShares lists the events whose participants chose the caller.
func (s *Service) ListSafetyShares(ctx context.Context, caller id.ID) ([]Share, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	now := s.now()
	rows, err := s.q.ListSafetyShares(ctx, store.ListSafetySharesParams{
		ContactID: caller.PG(), EndedAfter: ts(now.Add(-ShareAfterEnd)), AlertedAfter: ts(now.Add(-ShareAfterAlert)),
	})
	if err != nil {
		return nil, fmt.Errorf("güvenlik paylaşımları: %w", err)
	}
	out := make([]Share, len(rows))
	for i, r := range rows {
		out[i] = Share{
			Participant: Person{ID: idOf(r.UserID), DisplayName: r.DisplayName.String, AvatarID: idOf(r.AvatarUploadID)},
			TayfaName:   r.TayfaName, EventID: idOf(r.EventID), EventTitle: r.Title,
			StartsAt: r.StartsAt.Time, EndsAt: r.EndsAt.Time, MeetingPointName: r.MeetingPointName.String,
			City: r.City.String, District: r.District.String, GraceMinutes: r.GraceMinutes,
			ReturnedAt: r.ReturnedAt.Time, AlertSentAt: r.AlertSentAt.Time,
		}
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func idOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}
