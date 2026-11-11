// User methods of the notification center: list, read marks and muting.
// Every method works on the caller's own rows only: another user's
// notification is "not found", exactly like a missing one (IDOR).

package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func invalid(field string) error { return apperr.Validation(field) }

const (
	// Retention is how long a notification is kept (v14 "Saklama süreleri").
	Retention = 90 * 24 * time.Hour
	// MaxUnread bounds the unread count; clients show "99+" anyway.
	MaxUnread = 1000
	// MaxMuteAhead bounds a timed mute; longer means level none.
	MaxMuteAhead = 365 * 24 * time.Hour

	defaultPageSize = 50
	maxPageSize     = 100
	retentionBatch  = 1000
	retentionEvery  = time.Hour
)

// Preference levels (notification_preferences.level).
const (
	LevelAll      = "all"
	LevelMentions = "mentions"
	LevelNone     = "none"
)

// Scopes users can set (notification_preferences.scope_type). Channels
// come later.
const (
	ScopeTayfa = "tayfa"
	// ScopeDMConversation: a direct conversation of the user (issue #138).
	ScopeDMConversation = "dm_conversation"
)

// Notification is one notification of the caller.
type Notification struct {
	ID         id.ID
	Type       string
	Tayfa      id.ID // zero: not about a tayfa
	Actor      id.ID // zero: the system or a deleted account
	TargetType string
	Target     id.ID
	Question   id.ID  // answer_received: the answered question
	Subject    id.ID  // hike_overdue: the hiker who did not return
	Reason     string // tayfa_warned: inactive or few_members
	// Unread: dm_request and dm_message only, the caller's unread messages
	// in the conversation when the notice was last updated (1–100).
	Unread    int32
	ReadAt    time.Time
	CreatedAt time.Time
}

// Preference is one muting preference of the caller.
type Preference struct {
	ScopeType  string
	Scope      id.ID
	Level      string
	MutedUntil time.Time // zero: no time limit
	UpdatedAt  time.Time
}

// Memberships answers whether a user is an active member of a tayfa. chat
// asks core (MembershipService.GetMembership with the user's own token,
// ADR-0009): memberships are core's data, chat keeps no copy.
type Memberships interface {
	IsActiveMember(ctx context.Context, tayfa, user id.ID) (bool, error)
}

// Conversations answers whether a user sees a direct conversation as a
// party: the same rule as reading the conversation (dm.Service). Another
// user's conversation is "not found", exactly like a missing one (IDOR).
type Conversations interface {
	SeesConversation(ctx context.Context, conversation, user id.ID) (bool, error)
}

// Config wires a Service.
type Config struct {
	Pool          *pgxpool.Pool
	Log           *slog.Logger
	Members       Memberships
	Conversations Conversations
	Now           func() time.Time // optional, for tests
}

// Service serves the notification center.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Members == nil || cfg.Conversations == nil {
		return nil, errors.New("notification: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// List returns a page of the caller's notifications, newest first. token is
// the ID of the last notification of the previous page.
func (s *Service) List(ctx context.Context, caller id.ID, size int32, token string) ([]Notification, string, error) {
	n := int32(defaultPageSize)
	switch {
	case size < 0:
		return nil, "", invalid("page_size")
	case size > maxPageSize:
		n = maxPageSize
	case size > 0:
		n = size
	}
	after := pgtype.UUID{}
	if token != "" {
		v, ok := id.Parse(token)
		if !ok {
			return nil, "", invalid("page_token")
		}
		after = v.PG()
	}
	rows, err := store.New(s.cfg.Pool).ListNotifications(ctx, store.ListNotificationsParams{
		UserID: caller.PG(), AfterID: after, N: n + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("bildirimler: %w", err)
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		next = id.ID(rows[n-1].ID.Bytes).String()
	}
	out := make([]Notification, 0, len(rows))
	for _, r := range rows {
		v := Notification{
			ID: id.ID(r.ID.Bytes), Type: r.Type, TargetType: r.TargetType, Target: id.ID(r.TargetID.Bytes),
			CreatedAt: r.CreatedAt.Time.UTC(),
		}
		if r.TayfaID.Valid {
			v.Tayfa = id.ID(r.TayfaID.Bytes)
		}
		if r.ActorID.Valid {
			v.Actor = id.ID(r.ActorID.Bytes)
		}
		if r.ReadAt.Valid {
			v.ReadAt = r.ReadAt.Time.UTC()
		}
		if r.UnreadCount.Valid {
			v.Unread = r.UnreadCount.Int32
		}
		var p struct {
			QuestionID string `json:"question_id"`
			UserID     string `json:"user_id"`
			Reason     string `json:"reason"`
		}
		if len(r.Payload) > 0 && json.Unmarshal(r.Payload, &p) == nil {
			v.Question, _ = id.Parse(p.QuestionID)
			v.Subject, _ = id.Parse(p.UserID)
			v.Reason = p.Reason
		}
		out = append(out, v)
	}
	return out, next, nil
}

// UnreadCount returns the caller's unread notifications, at most MaxUnread.
func (s *Service) UnreadCount(ctx context.Context, caller id.ID) (int32, error) {
	n, err := store.New(s.cfg.Pool).CountUnreadNotifications(ctx, store.CountUnreadNotificationsParams{UserID: caller.PG(), MaxCount: MaxUnread})
	if err != nil {
		return 0, fmt.Errorf("okunmamış: %w", err)
	}
	return int32(n), nil //nolint:gosec // at most MaxUnread
}

// MarkRead marks one of the caller's notifications read. Marking again
// keeps the first read time.
func (s *Service) MarkRead(ctx context.Context, caller, notification id.ID) error {
	_, err := store.New(s.cfg.Pool).MarkNotificationRead(ctx, store.MarkNotificationReadParams{
		ID: notification.PG(), UserID: caller.PG(), Now: ts(s.now()),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotificationNotFound
	}
	if err != nil {
		return fmt.Errorf("okundu: %w", err)
	}
	return nil
}

// MarkAllRead marks all of the caller's notifications read.
func (s *Service) MarkAllRead(ctx context.Context, caller id.ID) error {
	if _, err := store.New(s.cfg.Pool).MarkAllNotificationsRead(ctx, store.MarkAllNotificationsReadParams{UserID: caller.PG(), Now: ts(s.now())}); err != nil {
		return fmt.Errorf("tümü okundu: %w", err)
	}
	return nil
}

// Preferences returns the caller's muting preferences.
func (s *Service) Preferences(ctx context.Context, caller id.ID) ([]Preference, error) {
	rows, err := store.New(s.cfg.Pool).ListNotificationPreferences(ctx, caller.PG())
	if err != nil {
		return nil, fmt.Errorf("tercihler: %w", err)
	}
	out := make([]Preference, 0, len(rows))
	for _, r := range rows {
		p := Preference{ScopeType: r.ScopeType, Scope: id.ID(r.ScopeID.Bytes), Level: r.Level, UpdatedAt: r.UpdatedAt.Time.UTC()}
		if r.MutedUntil.Valid {
			p.MutedUntil = r.MutedUntil.Time.UTC()
		}
		out = append(out, p)
	}
	return out, nil
}

// SetTayfaPreference sets how the caller hears about a tayfa they are an
// active member of: level all, mentions or none, optionally only until a
// time (then back to all). Level all without a time removes the preference.
// Muted notifications still appear in the list; the preference is read when
// sending push notifications.
func (s *Service) SetTayfaPreference(ctx context.Context, caller, tayfa id.ID, level string, until time.Time) (Preference, error) {
	return s.setPreference(ctx, caller, ScopeTayfa, tayfa, level, until, func() error {
		ok, err := s.cfg.Members.IsActiveMember(ctx, tayfa, caller)
		if err != nil {
			return fmt.Errorf("üyelik: %w", err)
		}
		if !ok {
			return apperr.ErrTayfaNotFound
		}
		return nil
	})
}

// SetConversationPreference sets how the caller hears about a direct
// conversation they see as a party (issue #138), with the rules of
// SetTayfaPreference. Any other conversation, another user's included, is
// apperr.ErrConversationNotFound and nothing is written. Direct message
// notices are all personal (ADR-0019 item 3): mentions lets them through
// like all.
func (s *Service) SetConversationPreference(ctx context.Context, caller, conversation id.ID, level string, until time.Time) (Preference, error) {
	return s.setPreference(ctx, caller, ScopeDMConversation, conversation, level, until, func() error {
		ok, err := s.cfg.Conversations.SeesConversation(ctx, conversation, caller)
		if err != nil {
			return fmt.Errorf("konuşma: %w", err)
		}
		if !ok {
			return apperr.ErrConversationNotFound
		}
		return nil
	})
}

// setPreference validates level and until, asks allowed whether the caller
// may set a preference for scope, and writes it (level all: removes it).
func (s *Service) setPreference(ctx context.Context, caller id.ID, scopeType string, scope id.ID, level string, until time.Time, allowed func() error) (Preference, error) {
	if level != LevelAll && level != LevelMentions && level != LevelNone {
		return Preference{}, invalid("level")
	}
	now := s.now()
	if !until.IsZero() {
		if level == LevelAll || !until.After(now) || until.After(now.Add(MaxMuteAhead)) {
			return Preference{}, invalid("mute_until")
		}
	}
	if err := allowed(); err != nil {
		return Preference{}, err
	}
	q := store.New(s.cfg.Pool)
	p := Preference{ScopeType: scopeType, Scope: scope, Level: level, MutedUntil: until.UTC(), UpdatedAt: now}
	var err error
	if level == LevelAll {
		err = q.DeleteNotificationPreference(ctx, store.DeleteNotificationPreferenceParams{UserID: caller.PG(), ScopeType: scopeType, ScopeID: scope.PG()})
	} else {
		mu := pgtype.Timestamptz{}
		if !until.IsZero() {
			mu = ts(until.UTC())
		}
		err = q.UpsertNotificationPreference(ctx, store.UpsertNotificationPreferenceParams{
			UserID: caller.PG(), ScopeType: scopeType, ScopeID: scope.PG(), Level: level, MutedUntil: mu,
		})
	}
	if err != nil {
		return Preference{}, fmt.Errorf("tercih: %w", err)
	}
	return p, nil
}

// PurgeOld deletes notifications older than Retention and returns how many.
func (s *Service) PurgeOld(ctx context.Context) (int64, error) {
	before := ts(s.now().Add(-Retention))
	var total int64
	for {
		n, err := store.New(s.cfg.Pool).DeleteOldNotifications(ctx, store.DeleteOldNotificationsParams{Before: before, N: retentionBatch})
		if err != nil {
			return total, fmt.Errorf("eski bildirimler: %w", err)
		}
		total += n
		if n < retentionBatch {
			return total, nil
		}
	}
}

// RunRetention runs PurgeOld every hour until ctx is done. It may run in
// more than one pod: deleting old rows twice is harmless.
func (s *Service) RunRetention(ctx context.Context) error {
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		if n, err := s.PurgeOld(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.WarnContext(ctx, "eski bildirimler silinemedi", slog.String("hata", err.Error()))
		} else if n > 0 {
			s.cfg.Log.InfoContext(ctx, "eski bildirimler silindi", slog.Int64("adet", n))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
