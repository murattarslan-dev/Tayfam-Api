// Package chatrelay is the outbox consumer that delivers core's events to
// chat's CoreEventService (ADR-0002, ADR-0009, issue #64).
//
// Only the event types in Types reach chat, an explicit list:
//
//   - notification.requested: core chose the recipients (package notify);
//     chat stores the in-app notifications.
//   - tayfa.purged: chat removes what it keeps about the tayfa (ADR-0009
//     "Tayfa → kanallar ve mesajları").
//
// Other rows of ADR-0009's table (tayfa.deleted, membership.changed,
// tayfa.status_changed) join the list with the channel work that needs
// them. Payloads carry IDs and classification only, and so do the
// requests.
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

// TypeTayfaPurged is the jobs package's tayfa.purged.
const TypeTayfaPurged = "tayfa.purged"

// Types returns the explicit list of events delivered to chat.
func Types() []string {
	return []string{notify.EventType, TypeTayfaPurged}
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
	notify.TypeHikeOverdue:            chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE,
}

var targets = map[string]chatv1.NotificationTargetType{
	notify.TargetTayfa:        chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA,
	notify.TargetMembership:   chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_MEMBERSHIP,
	notify.TargetAnnouncement: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANNOUNCEMENT,
	notify.TargetEvent:        chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT,
	notify.TargetAnswer:       chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER,
}

var reasons = map[string]chatv1.TayfaWarningReason{
	"inactive":    chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_INACTIVE,
	"few_members": chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_FEW_MEMBERS,
}

// Request converts a stored event to chat's request. An event it cannot
// convert is an error: the relay stops at it and retries rather than drop
// a notification (a new type must be added here and to chat's contract).
func Request(e outbox.Event) (*chatv1.ApplyCoreEventRequest, error) {
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, errors.New("olay içeriği çözümlenemedi")
	}
	req := &chatv1.ApplyCoreEventRequest{
		EventId: e.ID.String(), TayfaId: p.TayfaID, OccurredAt: timestamppb.New(e.OccurredAt),
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
	default:
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	return req, nil
}
