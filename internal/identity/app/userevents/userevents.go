// Package userevents writes identity's outbox events about a user (ADR-0002,
// ADR-0009): user.profile_updated keeps the profile copies in core and chat
// current, user.deleted makes them remove the user's personal records.
// session.revoked and user.sanctioned make chat close the real-time
// connections of a session or of every session of the user (ADR-0014,
// issue #92); they go to chat only.
//
// All are written inside the caller's transaction, as its last step, so the
// event exists exactly when the change it describes is committed.
package userevents

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// Event types; consumers match them exactly.
const (
	TypeProfileUpdated = "user.profile_updated"
	TypeDeleted        = "user.deleted"
	TypeSessionRevoked = "session.revoked"
	TypeSanctioned     = "user.sanctioned"
	subjectUser        = "user"
)

// Profile is the public profile carried by user.profile_updated: what other
// users see next to the user's content, and who may see the user's
// attendance history (core shows the profile, issue #71). Nothing else
// about the user is sent.
type Profile struct {
	DisplayName string `json:"display_name"`
	// AvatarUploadID is a core upload ID, empty when there is no avatar.
	AvatarUploadID string `json:"avatar_upload_id,omitempty"`
	// ActivityVisibility is users.activity_visibility: private, tayfa or
	// public. Empty in events written before it was added; core then keeps
	// its default (tayfa).
	ActivityVisibility string `json:"activity_visibility,omitempty"`
}

// ProfileUpdated records that user's public profile is now p.
func ProfileUpdated(ctx context.Context, tx pgx.Tx, user id.ID, p Profile) error {
	if p.DisplayName == "" {
		return fmt.Errorf("profil olayı: görünen ad boş")
	}
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{
		Type: TypeProfileUpdated, Actor: user,
		SubjectType: subjectUser, SubjectID: user, Payload: p,
	}); err != nil {
		return fmt.Errorf("profil olayı: %w", err)
	}
	return nil
}

// Deleted records that user's account is deleted. Earlier profile events of
// the user are removed from the outbox, since they carry the display name,
// and the user is cleared as the actor of earlier events (data model v14).
// The event itself has no actor and no payload: the ID is all consumers
// need.
func Deleted(ctx context.Context, tx pgx.Tx, user id.ID) error {
	q := store.New(tx)
	if err := q.DeleteUserProfileEvents(ctx, user.PG()); err != nil {
		return fmt.Errorf("eski profil olayları: %w", err)
	}
	if err := q.ClearEventActor(ctx, user.PG()); err != nil {
		return fmt.Errorf("olay aktörü: %w", err)
	}
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{
		Type: TypeDeleted, SubjectType: subjectUser, SubjectID: user,
	}); err != nil {
		return fmt.Errorf("silme olayı: %w", err)
	}
	return nil
}

// Revoked is the payload of session.revoked: the session ID only. The user
// is the event's subject; the reason (sign-out, token reuse, …) is not sent.
type Revoked struct {
	SessionID string `json:"session_id"`
}

// SessionsRevoked records that sessions of user are ended, one
// session.revoked event per session: the payload stays a fixed few bytes
// however many sessions end at once. No sessions writes nothing.
func SessionsRevoked(ctx context.Context, tx pgx.Tx, user id.ID, sessions ...id.ID) error {
	for _, s := range sessions {
		if s == (id.ID{}) {
			return fmt.Errorf("oturum olayı: oturum kimliği boş")
		}
		if _, err := outbox.Write(ctx, tx, outbox.NewEvent{
			Type: TypeSessionRevoked, SubjectType: subjectUser, SubjectID: user,
			Payload: Revoked{SessionID: s.String()},
		}); err != nil {
			return fmt.Errorf("oturum olayı: %w", err)
		}
	}
	return nil
}

// Sanctioned records that a platform sanction (suspension or ban) ended
// every session of user (ADR-0004 "Platform yaptırımları"). The payload is
// empty: which sanction, and why, stays in identity. Nothing calls it yet;
// the platform panel's sanction flow will, in the transaction that ends the
// sessions.
func Sanctioned(ctx context.Context, tx pgx.Tx, user id.ID) error {
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{
		Type: TypeSanctioned, SubjectType: subjectUser, SubjectID: user,
	}); err != nil {
		return fmt.Errorf("yaptırım olayı: %w", err)
	}
	return nil
}
