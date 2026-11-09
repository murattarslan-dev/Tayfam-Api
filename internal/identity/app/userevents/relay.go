package userevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

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
		req.Event = &corev1.ApplyUserEventRequest_ProfileUpdated{ProfileUpdated: &corev1.UserProfileUpdated{
			DisplayName: p.DisplayName, AvatarUploadId: p.AvatarUploadID,
		}}
	case TypeDeleted:
		req.Event = &corev1.ApplyUserEventRequest_Deleted{Deleted: &corev1.UserDeleted{}}
	default:
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	return req, nil
}
