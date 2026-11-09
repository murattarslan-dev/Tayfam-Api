// Package notification is chat's in-app notification center (data model v14
// "Bildirimler", issue #64).
//
// Following ADR-0009, core decides which users hear about a change: it owns
// memberships and sends the recipients in notification.requested events.
// chat writes one row per recipient (Store) and keeps no copy of
// memberships. A notification holds IDs and classification only: type,
// tayfa, actor, target and, where needed, one extra ID or a reason. Titles,
// bodies, places and names are never stored; clients read the content from
// core, which checks access again.
package notification

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Notification types (data model v14 "Senaryo Kuralları → Bildirimler",
// plus the background job notices of issue #66).
const (
	TypeEventCreated           = "event_created"
	TypeEventUpdated           = "event_updated"
	TypeEventCancelled         = "event_cancelled"
	TypeRSVPPromoted           = "rsvp_promoted"
	TypeAnnouncement           = "announcement"
	TypeAnswerReceived         = "answer_received"
	TypeJoinRequest            = "join_request"
	TypeJoinApproved           = "join_approved"
	TypeTayfaDeletionRequested = "tayfa_deletion_requested"
	TypeTayfaWarned            = "tayfa_warned"
	TypeTayfaArchived          = "tayfa_archived"
	TypeTayfaSuspended         = "tayfa_suspended"
	TypeTayfaUnsuspended       = "tayfa_unsuspended"
	// TypeHikeOverdue cannot be muted (v14): push will send it whatever the
	// preference.
	TypeHikeOverdue = "hike_overdue"
	// Social notifications (issue #71). The two friend types are not about
	// a tayfa.
	TypeFriendRequest   = "friend_request"
	TypeFriendAccepted  = "friend_accepted"
	TypeTayfaInvitation = "tayfa_invitation"
	// TypeReportResolved: a report the recipient filed was resolved (issue
	// #76). It never has an actor.
	TypeReportResolved = "report_resolved"
	// Direct message notices (issue #95). chat writes them itself (its own
	// data); they say only that there is something new: no actor, no
	// content, not about a tayfa (ADR-0005, v14).
	TypeDMRequest = "dm_request"
	TypeDMMessage = "dm_message"
)

// Target types: what the client opens (and reads from core).
const (
	TargetTayfa        = "tayfa"
	TargetMembership   = "membership"
	TargetAnnouncement = "announcement"
	TargetEvent        = "event"
	TargetAnswer       = "answer"
	TargetUser         = "user"
	TargetReport       = "report"
	// TargetDMConversation: a conversation of the recipient.
	TargetDMConversation = "dm_conversation"
)

// Reasons of TypeTayfaWarned.
const (
	ReasonInactive   = "inactive"
	ReasonFewMembers = "few_members"
)

// MaxRecipients bounds one request (core sends at most 150).
const MaxRecipients = 200

// targetOf is the only target type each notification type may have.
func targetOf(typ string) (string, bool) {
	switch typ {
	case TypeEventCreated, TypeEventUpdated, TypeEventCancelled, TypeRSVPPromoted, TypeHikeOverdue:
		return TargetEvent, true
	case TypeAnnouncement:
		return TargetAnnouncement, true
	case TypeAnswerReceived:
		return TargetAnswer, true
	case TypeJoinRequest:
		return TargetMembership, true
	case TypeReportResolved:
		return TargetReport, true
	case TypeJoinApproved, TypeTayfaDeletionRequested, TypeTayfaWarned, TypeTayfaArchived, TypeTayfaInvitation,
		TypeTayfaSuspended, TypeTayfaUnsuspended:
		return TargetTayfa, true
	case TypeFriendRequest, TypeFriendAccepted:
		return TargetUser, true
	case TypeDMRequest, TypeDMMessage:
		return TargetDMConversation, true
	}
	return "", false
}

// withoutTayfa reports whether a notification type is not about a tayfa.
func withoutTayfa(typ string) bool {
	return typ == TypeFriendRequest || typ == TypeFriendAccepted || IsDirect(typ)
}

// IsDirect reports whether a notification type is about a direct message.
func IsDirect(typ string) bool { return typ == TypeDMRequest || typ == TypeDMMessage }

// Request is one notification.requested event of core.
type Request struct {
	Event      id.ID // core's outbox event ID: one row per event and recipient
	Tayfa      id.ID // zero only for the types withoutTayfa
	Actor      id.ID // zero: the system
	Type       string
	TargetType string
	Target     id.ID
	Recipients []id.ID
	// Extra IDs and classification, by type.
	Question id.ID  // answer_received
	Subject  id.ID  // hike_overdue: the hiker
	Reason   string // tayfa_warned
	// Unread is the recipient's unread messages in the conversation, for
	// the direct message types only (1–MaxDirectUnread).
	Unread int32
}

// MaxDirectUnread caps a direct message notice's unread count; the client
// shows 100 as "99+" (issue #101).
const MaxDirectUnread = 100

// Validate checks a request against the rules of its type.
func (r Request) Validate() error {
	switch {
	case r.Event == (id.ID{}):
		return apperr.Validation("event_id")
	case (r.Tayfa == id.ID{}) != withoutTayfa(r.Type):
		return apperr.Validation("tayfa_id")
	case r.Target == (id.ID{}):
		return apperr.Validation("notification_requested.target_id")
	case len(r.Recipients) == 0 || len(r.Recipients) > MaxRecipients:
		return apperr.Validation("notification_requested.recipient_ids")
	}
	target, ok := targetOf(r.Type)
	if !ok {
		return apperr.Validation("notification_requested.type")
	}
	if r.TargetType != target {
		return apperr.Validation("notification_requested.target_type")
	}
	if (r.Type == TypeAnswerReceived) != (r.Question != id.ID{}) {
		return apperr.Validation("notification_requested.question_id")
	}
	if (r.Type == TypeHikeOverdue) != (r.Subject != id.ID{}) {
		return apperr.Validation("notification_requested.subject_user_id")
	}
	// The reporter is never told who handled their report; a direct
	// message notice does not say who wrote.
	if (r.Type == TypeReportResolved || IsDirect(r.Type)) && r.Actor != (id.ID{}) {
		return apperr.Validation("actor_id")
	}
	if IsDirect(r.Type) != (r.Unread > 0) || r.Unread > MaxDirectUnread {
		return apperr.Validation("unread_count")
	}
	if r.Type == TypeTayfaWarned {
		if r.Reason != ReasonInactive && r.Reason != ReasonFewMembers {
			return apperr.Validation("notification_requested.warning_reason")
		}
	} else if r.Reason != "" {
		return apperr.Validation("notification_requested.warning_reason")
	}
	for _, u := range r.Recipients {
		if u == (id.ID{}) || u == r.Actor {
			return apperr.Validation("notification_requested.recipient_ids")
		}
	}
	return nil
}

// payload is the extra IDs and classification of a notification.
type payload struct {
	QuestionID string `json:"question_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Store writes one notification per recipient of r, once: the same event
// delivered again adds nothing, and a recipient whose account was deleted
// gets nothing.
func Store(ctx context.Context, q *store.Queries, r Request) error {
	if err := r.Validate(); err != nil {
		return err
	}
	var p payload
	if r.Question != (id.ID{}) {
		p.QuestionID = r.Question.String()
	}
	if r.Subject != (id.ID{}) {
		p.UserID = r.Subject.String()
	}
	p.Reason = r.Reason
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("bildirim içeriği: %w", err)
	}
	ids := make([]pgtype.UUID, len(r.Recipients))
	users := make([]pgtype.UUID, len(r.Recipients))
	for i, u := range r.Recipients {
		ids[i], users[i] = id.New().PG(), u.PG()
	}
	if _, err := q.CreateNotifications(ctx, store.CreateNotificationsParams{
		TayfaID: optional(r.Tayfa), Type: r.Type, ActorID: optional(r.Actor),
		TargetType: r.TargetType, TargetID: r.Target.PG(), Payload: raw,
		SourceEventID: r.Event.PG(), Ids: ids, UserIds: users, UnreadCount: unread(r.Unread),
	}); err != nil {
		return fmt.Errorf("bildirimler: %w", err)
	}
	return nil
}

// PurgeTayfa removes a purged tayfa's notifications and preferences.
func PurgeTayfa(ctx context.Context, q *store.Queries, tayfa id.ID) error {
	if err := q.DeleteTayfaNotifications(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("tayfa bildirimleri: %w", err)
	}
	if err := q.DeleteTayfaNotificationPreferences(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("tayfa tercihleri: %w", err)
	}
	return nil
}

// Cleanup removes a deleted user's notifications and preferences and clears
// them as the actor of others' notifications (ADR-0009). It is a
// userevents.Cleanup and runs in the deletion's transaction.
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	if err := q.DeleteUserNotifications(ctx, user.PG()); err != nil {
		return fmt.Errorf("bildirimler: %w", err)
	}
	if err := q.ClearNotificationActor(ctx, user.PG()); err != nil {
		return fmt.Errorf("bildirim aktörü: %w", err)
	}
	if err := q.DeleteUserNotificationPreferences(ctx, user.PG()); err != nil {
		return fmt.Errorf("bildirim tercihleri: %w", err)
	}
	return nil
}

func unread(n int32) pgtype.Int4 {
	if n == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: n, Valid: true}
}

func optional(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}
