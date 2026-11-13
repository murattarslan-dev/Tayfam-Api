// Package chatrelay is the outbox consumer that delivers core's events to
// chat's CoreEventService (ADR-0002, ADR-0009, issue #64).
//
// Only the event types in Types reach chat, an explicit list:
//
//   - notification.requested: core chose the recipients (package notify);
//     chat stores the in-app notifications.
//   - tayfa.purged: chat removes what it keeps about the tayfa (ADR-0009
//     "Tayfa → kanallar ve mesajları").
//   - membership.changed: chat drops the channel permissions it keeps for
//     a few seconds for that member (issue #81; ADR-0009 "Üyelik
//     değişiklikleri"). Only the user ID goes, not the kind of change.
//   - tayfa.warned, tayfa.archived, tayfa.reactivated, tayfa.deleted,
//     tayfa.deletion_requested, tayfa.deletion_cancelled, tayfa.suspended,
//     tayfa.unsuspended (platform panel, issue #104): the tayfa's
//     read-only state may have changed (ADR-0009 "Tayfa askı ve arşiv");
//     chat drops the permissions it keeps for the tayfa. They reach chat as
//     one kind, tayfa_status_changed.
//
// Payloads carry IDs and classification only, and so do the requests.
package chatrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// ConsumerName is chat's position in core's event_consumers.
const ConsumerName = "chat"

// deliverTimeout bounds one call to chat; the relay retries afterwards.
const deliverTimeout = 10 * time.Second

// Event types delivered to chat besides notification.requested. They are
// the jobs, membership and tayfa packages' events, named here so this
// package does not depend on them.
const (
	TypeTayfaPurged            = "tayfa.purged"
	TypeMembershipChanged      = "membership.changed"
	TypeTayfaWarned            = "tayfa.warned"
	TypeTayfaArchived          = "tayfa.archived"
	TypeTayfaReactivated       = "tayfa.reactivated"
	TypeTayfaDeleted           = "tayfa.deleted"
	TypeTayfaDeletionRequested = "tayfa.deletion_requested"
	TypeTayfaDeletionCancelled = "tayfa.deletion_cancelled"
	// Platform suspension (issue #104, package admin).
	TypeTayfaSuspended   = "tayfa.suspended"
	TypeTayfaUnsuspended = "tayfa.unsuspended"
	// Event channels (issue #183, package event): who is in them and when
	// they are archived.
	TypeRSVPChanged    = "rsvp.changed"
	TypeRSVPPromoted   = "rsvp.promoted"
	TypeEventUpdated   = "event.updated"
	TypeEventCancelled = "event.cancelled"
)

// endedChanges are the membership.changed kinds after which the user is no
// longer a member (package membership).
var endedChanges = map[string]bool{"left": true, "removed": true, "banned": true}

// statusTypes change whether the tayfa is writable.
var statusTypes = map[string]bool{
	TypeTayfaWarned: true, TypeTayfaArchived: true, TypeTayfaReactivated: true, TypeTayfaDeleted: true,
	TypeTayfaDeletionRequested: true, TypeTayfaDeletionCancelled: true,
	TypeTayfaSuspended: true, TypeTayfaUnsuspended: true,
}

// Types returns the explicit list of events delivered to chat.
func Types() []string {
	return []string{
		notify.EventType, TypeTayfaPurged, TypeMembershipChanged,
		TypeTayfaWarned, TypeTayfaArchived, TypeTayfaReactivated, TypeTayfaDeleted,
		TypeTayfaDeletionRequested, TypeTayfaDeletionCancelled,
		TypeTayfaSuspended, TypeTayfaUnsuspended,
		TypeRSVPChanged, TypeRSVPPromoted, TypeEventUpdated, TypeEventCancelled,
	}
}

// Consumer returns the outbox consumer for chat. client must carry core's
// service token for chat (auth.ServiceTokenFrom, ADR-0013).
func Consumer(client chatv1.CoreEventServiceClient) outbox.Consumer {
	return outbox.Consumer{
		Name:  ConsumerName,
		Types: Types(),
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := Request(e)
			if err != nil {
				return err
			}
			if req == nil {
				return nil // nothing chat needs (see Request)
			}
			ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
			defer cancel()
			if _, err := client.ApplyCoreEvent(ctx, req); err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			return nil
		},
	}
}

type payload struct {
	TayfaID      string   `json:"tayfa_id"`
	Type         string   `json:"type"`
	TargetType   string   `json:"target_type"`
	TargetID     string   `json:"target_id"`
	RecipientIDs []string `json:"recipient_ids"`
	QuestionID   string   `json:"question_id"`
	UserID       string   `json:"user_id"`
	Reason       string   `json:"reason"`
	// Change is a membership.changed kind.
	Change string `json:"change"`
	// Event channels (issue #183).
	EventID     string     `json:"event_id"`
	EndsAt      *time.Time `json:"ends_at"`
	CancelledAt *time.Time `json:"cancelled_at"`
}

var types = map[string]chatv1.NotificationType{
	notify.TypeEventCreated:           chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CREATED,
	notify.TypeEventUpdated:           chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_UPDATED,
	notify.TypeEventCancelled:         chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CANCELLED,
	notify.TypeRSVPPromoted:           chatv1.NotificationType_NOTIFICATION_TYPE_RSVP_PROMOTED,
	notify.TypeAnnouncement:           chatv1.NotificationType_NOTIFICATION_TYPE_ANNOUNCEMENT,
	notify.TypeAnswerReceived:         chatv1.NotificationType_NOTIFICATION_TYPE_ANSWER_RECEIVED,
	notify.TypeJoinRequest:            chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_REQUEST,
	notify.TypeJoinApproved:           chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_APPROVED,
	notify.TypeTayfaDeletionRequested: chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_DELETION_REQUESTED,
	notify.TypeTayfaWarned:            chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_WARNED,
	notify.TypeTayfaArchived:          chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_ARCHIVED,
	notify.TypeTayfaSuspended:         chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_SUSPENDED,
	notify.TypeTayfaUnsuspended:       chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_UNSUSPENDED,
	notify.TypeHikeOverdue:            chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE,
	notify.TypeFriendRequest:          chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_REQUEST,
	notify.TypeFriendAccepted:         chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_ACCEPTED,
	notify.TypeTayfaInvitation:        chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_INVITATION,
	notify.TypeReportResolved:         chatv1.NotificationType_NOTIFICATION_TYPE_REPORT_RESOLVED,
}

var targets = map[string]chatv1.NotificationTargetType{
	notify.TargetTayfa:        chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA,
	notify.TargetMembership:   chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_MEMBERSHIP,
	notify.TargetAnnouncement: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANNOUNCEMENT,
	notify.TargetEvent:        chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT,
	notify.TargetAnswer:       chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER,
	notify.TargetUser:         chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_USER,
	notify.TargetReport:       chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_REPORT,
}

var reasons = map[string]chatv1.TayfaWarningReason{
	"inactive":    chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_INACTIVE,
	"few_members": chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_FEW_MEMBERS,
}

// Request converts a stored event to chat's request. An event it cannot
// convert is an error: the relay stops at it and retries rather than drop
// a notification (a new type must be added here and to chat's contract).
// An event.updated that did not move the event's end is nil: chat needs
// nothing from it.
func Request(e outbox.Event) (*chatv1.ApplyCoreEventRequest, error) {
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, errors.New("olay içeriği çözümlenemedi")
	}
	tayfa := p.TayfaID
	// The tayfa package's events name the tayfa as their subject only.
	if tayfa == "" && e.SubjectType == "tayfa" {
		tayfa = e.SubjectID.String()
	}
	req := &chatv1.ApplyCoreEventRequest{
		EventId: e.ID.String(), TayfaId: tayfa, OccurredAt: timestamppb.New(e.OccurredAt),
	}
	if e.Actor != (id.ID{}) {
		req.ActorId = e.Actor.String()
	}
	switch e.Type {
	case notify.EventType:
		typ, ok := types[p.Type]
		if !ok {
			return nil, fmt.Errorf("bilinmeyen bildirim türü %q", p.Type)
		}
		target, ok := targets[p.TargetType]
		if !ok {
			return nil, fmt.Errorf("bilinmeyen bildirim hedefi %q", p.TargetType)
		}
		n := &chatv1.NotificationRequested{
			Type: typ, TargetType: target, TargetId: p.TargetID, RecipientIds: p.RecipientIDs,
			QuestionId: p.QuestionID, SubjectUserId: p.UserID,
		}
		if p.Reason != "" {
			if n.WarningReason, ok = reasons[p.Reason]; !ok {
				return nil, fmt.Errorf("bilinmeyen uyarı nedeni %q", p.Reason)
			}
		}
		req.Event = &chatv1.ApplyCoreEventRequest_NotificationRequested{NotificationRequested: n}
	case TypeTayfaPurged:
		req.Event = &chatv1.ApplyCoreEventRequest_TayfaPurged{TayfaPurged: &chatv1.TayfaPurged{}}
	case TypeRSVPChanged, TypeRSVPPromoted:
		if _, ok := id.Parse(p.EventID); !ok {
			return nil, errors.New("katılım olayında etkinlik yok")
		}
		if _, ok := id.Parse(p.UserID); !ok {
			return nil, errors.New("katılım olayında kullanıcı yok")
		}
		// The answer itself is not sent: chat asks core again.
		req.Event = &chatv1.ApplyCoreEventRequest_EventAttendanceChanged{EventAttendanceChanged: &chatv1.EventAttendanceChanged{
			EventId: p.EventID, UserId: p.UserID,
		}}
	case TypeEventUpdated, TypeEventCancelled:
		if _, ok := id.Parse(p.EventID); !ok {
			return nil, errors.New("etkinlik olayında etkinlik yok")
		}
		end, cancelled := p.EndsAt, false
		if e.Type == TypeEventCancelled {
			end, cancelled = p.CancelledAt, true
			if end == nil {
				// Written before the time was added: the event's own time.
				t := e.OccurredAt
				end = &t
			}
		}
		if end == nil {
			return nil, nil //nolint:nilnil // nothing chat needs: the end did not move
		}
		req.Event = &chatv1.ApplyCoreEventRequest_EventScheduleChanged{EventScheduleChanged: &chatv1.EventScheduleChanged{
			EventId: p.EventID, EndTime: timestamppb.New(*end), Cancelled: cancelled,
		}}
	case TypeMembershipChanged:
		if _, ok := id.Parse(p.UserID); !ok {
			return nil, errors.New("üyelik olayında kullanıcı yok")
		}
		req.Event = &chatv1.ApplyCoreEventRequest_MembershipChanged{MembershipChanged: &chatv1.MembershipChanged{
			UserId: p.UserID, Ended: endedChanges[p.Change],
		}}
	default:
		if statusTypes[e.Type] {
			req.Event = &chatv1.ApplyCoreEventRequest_TayfaStatusChanged{TayfaStatusChanged: &chatv1.TayfaStatusChanged{}}
			break
		}
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	if req.GetTayfaId() == "" && req.GetNotificationRequested() == nil {
		return nil, errors.New("olayda tayfa yok")
	}
	return req, nil
}
