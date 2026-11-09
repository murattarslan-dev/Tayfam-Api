package userevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// deliverTimeout bounds one call to a target; the relay retries afterwards.
const deliverTimeout = 10 * time.Second

// CoreConsumer is the outbox consumer that delivers user events to core's
// UserEventService (ADR-0009). client must carry identity's service token
// for core (auth.ServiceTokenFrom, ADR-0013).
func CoreConsumer(client corev1.UserEventServiceClient) outbox.Consumer {
	return outbox.Consumer{
		Name:  "core",
		Types: []string{TypeProfileUpdated, TypeDeleted},
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := coreRequest(e)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
			defer cancel()
			if _, err := client.ApplyUserEvent(ctx, req); err != nil {
				return fmt.Errorf("core: %w", err)
			}
			return nil
		},
	}
}

// coreVisibility maps users.activity_visibility to core's enum. Events
// written before the field existed carry none.
var coreVisibility = map[string]corev1.ActivityVisibility{
	"":        corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED,
	"private": corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE,
	"tayfa":   corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
	"public":  corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC,
}

// coreRequest converts a stored event to core's request.
func coreRequest(e outbox.Event) (*corev1.ApplyUserEventRequest, error) {
	if e.SubjectType != subjectUser {
		return nil, errors.New("olayın konusu kullanıcı değil")
	}
	req := &corev1.ApplyUserEventRequest{
		EventId:    e.ID.String(),
		UserId:     e.SubjectID.String(),
		OccurredAt: timestamppb.New(e.OccurredAt),
	}
	switch e.Type {
	case TypeProfileUpdated:
		var p Profile
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, errors.New("profil olayı çözümlenemedi")
		}
		vis, ok := coreVisibility[p.ActivityVisibility]
		if !ok {
			return nil, fmt.Errorf("bilinmeyen görünürlük %q", p.ActivityVisibility)
		}
		req.Event = &corev1.ApplyUserEventRequest_ProfileUpdated{ProfileUpdated: &corev1.UserProfileUpdated{
			DisplayName: p.DisplayName, AvatarUploadId: p.AvatarUploadID, ActivityVisibility: vis,
		}}
	case TypeDeleted:
		req.Event = &corev1.ApplyUserEventRequest_Deleted{Deleted: &corev1.UserDeleted{}}
	default:
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	return req, nil
}

// ChatConsumer is the outbox consumer that delivers user events to chat's
// UserEventService (ADR-0009, issue #64). client must carry identity's
// service token for chat (auth.ServiceTokenFrom, ADR-0013). It has its own
// position: chat being down does not hold up core.
func ChatConsumer(client chatv1.UserEventServiceClient) outbox.Consumer {
	return outbox.Consumer{
		Name:  "chat",
		Types: []string{TypeProfileUpdated, TypeDeleted},
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := chatRequest(e)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
			defer cancel()
			if _, err := client.ApplyUserEvent(ctx, req); err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			return nil
		},
	}
}

// chatRequest converts a stored event to chat's request; it carries the
// same fields as core's.
func chatRequest(e outbox.Event) (*chatv1.ApplyUserEventRequest, error) {
	c, err := coreRequest(e)
	if err != nil {
		return nil, err
	}
	req := &chatv1.ApplyUserEventRequest{EventId: c.GetEventId(), UserId: c.GetUserId(), OccurredAt: c.GetOccurredAt()}
	switch ev := c.GetEvent().(type) {
	case *corev1.ApplyUserEventRequest_ProfileUpdated:
		req.Event = &chatv1.ApplyUserEventRequest_ProfileUpdated{ProfileUpdated: &chatv1.UserProfileUpdated{
			DisplayName: ev.ProfileUpdated.GetDisplayName(), AvatarUploadId: ev.ProfileUpdated.GetAvatarUploadId(),
		}}
	case *corev1.ApplyUserEventRequest_Deleted:
		req.Event = &chatv1.ApplyUserEventRequest_Deleted{Deleted: &chatv1.UserDeleted{}}
	}
	return req, nil
}
