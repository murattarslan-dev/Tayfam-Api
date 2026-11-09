// Package userevents applies identity's user events to chat (ADR-0009):
// user.profile_updated keeps the profile copy current and user.deleted
// removes the user's records from chat.
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
	// Exactly one of the two.
	Profile *Profile
	Deleted bool
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
	switch {
	case e.Profile != nil && !e.Deleted:
		return s.applyProfile(ctx, e)
	case e.Profile == nil && e.Deleted:
		return s.applyDeleted(ctx, e)
	}
	return apperr.Validation("event")
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
