// Package userevents applies identity's user events to chat (ADR-0009):
// user.profile_updated keeps the profile copy current and user.deleted
// removes the user's records from chat. session.revoked, user.sanctioned
// and user.deleted also end the user's real-time connections (issue #82),
// end-to-end devices (issue #127) and push tokens (issue #134).
//
// Events arrive at least once and may arrive out of order, so every apply is
// idempotent: applying the same event again, or an older one after a newer,
// leaves the same result (ADR-0002).
package userevents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event is one identity event about a user.
type Event struct {
	ID         id.ID // identity's outbox event ID (UUIDv7)
	User       id.ID
	OccurredAt time.Time
	// Exactly one of these.
	Profile *Profile
	Deleted bool
	// SessionRevoked is the session of a session.revoked.
	SessionRevoked id.ID
	// Sanctioned: user.sanctioned closed every session of the user.
	Sanctioned bool
}

// Sessions ends sessions in chat's real-time layer
// (realtime.Hub): their connections close.
type Sessions interface {
	EndSession(ctx context.Context, session id.ID) error
	EndUser(ctx context.Context, user id.ID) error
}

// Devices ends end-to-end devices (keys.Service; issue #127, ADR-0018): a
// device is a session, so it goes when its session does.
type Devices interface {
	SessionEnded(ctx context.Context, session id.ID) error
	UserSanctioned(ctx context.Context, user id.ID, at time.Time) error
}

// Profile is the public profile carried by user.profile_updated.
type Profile struct {
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
}

// Cleanup removes a deleted user's records that other packages own (the
// notifications of issue #64), inside the deletion's transaction.
type Cleanup func(ctx context.Context, q *store.Queries, user id.ID) error

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Cleanups run after the copies are removed; optional.
	Cleanups []Cleanup
	// Sessions ends connections; optional (without it session events are
	// accepted and change nothing).
	Sessions Sessions
	// Devices removes the end-to-end devices of ended sessions; optional.
	// A deleted user's devices go with Cleanups (keys.Cleanup).
	Devices Devices
	// PushTokens removes the push tokens of ended sessions the same way
	// (push.Service; issue #134, ADR-0019): a token is a session too.
	// Optional; a deleted user's tokens go with Cleanups (push.Cleanup).
	PushTokens Devices
}

// Service applies user events.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("userevents: eksik yapılandırma")
	}
	return &Service{cfg: cfg}, nil
}

// Apply applies e.
func (s *Service) Apply(ctx context.Context, e Event) error {
	if e.ID == (id.ID{}) {
		return apperr.Validation("event_id")
	}
	if e.User == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	kinds := 0
	for _, set := range []bool{e.Profile != nil, e.Deleted, e.SessionRevoked != (id.ID{}), e.Sanctioned} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return apperr.Validation("event")
	}
	switch {
	case e.Profile != nil:
		return s.applyProfile(ctx, e)
	case e.Deleted:
		if err := s.applyDeleted(ctx, e); err != nil {
			return err
		}
		return s.endUser(ctx, e.User)
	case e.Sanctioned:
		if s.cfg.Devices != nil {
			if err := s.cfg.Devices.UserSanctioned(ctx, e.User, e.OccurredAt); err != nil {
				return fmt.Errorf("e2e cihazları: %w", err)
			}
		}
		if s.cfg.PushTokens != nil {
			if err := s.cfg.PushTokens.UserSanctioned(ctx, e.User, e.OccurredAt); err != nil {
				return fmt.Errorf("push token'ları: %w", err)
			}
		}
		return s.endUser(ctx, e.User)
	}
	if s.cfg.Devices != nil {
		if err := s.cfg.Devices.SessionEnded(ctx, e.SessionRevoked); err != nil {
			return fmt.Errorf("e2e cihazı: %w", err)
		}
	}
	if s.cfg.PushTokens != nil {
		if err := s.cfg.PushTokens.SessionEnded(ctx, e.SessionRevoked); err != nil {
			return fmt.Errorf("push token'ı: %w", err)
		}
	}
	if s.cfg.Sessions == nil {
		return nil
	}
	if err := s.cfg.Sessions.EndSession(ctx, e.SessionRevoked); err != nil {
		return fmt.Errorf("oturum kapatılamadı: %w", err)
	}
	return nil
}

func (s *Service) endUser(ctx context.Context, user id.ID) error {
	if s.cfg.Sessions == nil {
		return nil
	}
	if err := s.cfg.Sessions.EndUser(ctx, user); err != nil {
		return fmt.Errorf("oturumlar kapatılamadı: %w", err)
	}
	return nil
}

func (s *Service) applyProfile(ctx context.Context, e Event) error {
	name := e.Profile.DisplayName
	if !validDisplayName(name) {
		return apperr.Validation("profile_updated.display_name")
	}
	avatar := pgtype.UUID{}
	if e.Profile.AvatarUploadID != (id.ID{}) {
		avatar = e.Profile.AvatarUploadID.PG()
	}
	// 0 rows: a newer event or the deletion was applied already. That is
	// the expected result of a repeat, not an error.
	if _, err := store.New(s.cfg.Pool).UpsertUserProfile(ctx, store.UpsertUserProfileParams{
		UserID: e.User.PG(), DisplayName: pgtype.Text{String: name, Valid: true},
		AvatarUploadID: avatar, SourceEventID: e.ID.PG(),
	}); err != nil {
		return fmt.Errorf("profil kopyası: %w", err)
	}
	return nil
}

// validDisplayName mirrors identity's rule (1–40 characters, no control
// characters, normalised spaces) and user_profiles_display_name_len.
func validDisplayName(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 40 || !utf8.ValidString(s) || strings.Join(strings.Fields(s), " ") != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}

// applyDeleted removes the user from chat in one transaction: the profile
// copy becomes a tombstone (no notification is written for the user any
// more), then the cleanups run (the user's notifications and preferences).
func (s *Service) applyDeleted(ctx context.Context, e Event) error {
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.TombstoneUserProfile(ctx, store.TombstoneUserProfileParams{UserID: e.User.PG(), SourceEventID: e.ID.PG()}); err != nil {
			return fmt.Errorf("profil kopyası: %w", err)
		}
		for _, c := range s.cfg.Cleanups {
			if err := c(ctx, q, e.User); err != nil {
				return err
			}
		}
		return nil
	})
}
