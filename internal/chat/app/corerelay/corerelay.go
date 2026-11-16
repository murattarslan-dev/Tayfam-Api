// Package corerelay delivers chat's events to core (ADR-0002, ADR-0013,
// ADR-0031): chat's first outbox consumer outside chat. Only the types in
// Types reach core; every other chat event stays in chat's outbox for
// history and counting.
//
//   - chat.media_released: a channel or direct message no longer holds its
//     photo or attachment (the author deleted it, or it was deleted for
//     good). core deletes the upload if it is still attached to that
//     message.
//
// The payload carries IDs only. The relay calls core's ChatEventService
// with chat's service token (audience core.tayfam.internal); core accepts
// it from the tayfam-chat ServiceAccount only.
package corerelay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// TypeMediaReleased is the event a message writes when it lets go of its
// file, in the same transaction as the change.
const TypeMediaReleased = "chat.media_released"

// Types are the event types delivered to core.
var Types = []string{TypeMediaReleased}

// ConsumerName is the relay's position in event_consumers.
const ConsumerName = "core"

// Release writes chat.media_released for upload and message inside tx.
// Call it as the last statement of the transaction (outbox ordering).
func Release(ctx context.Context, tx pgx.Tx, upload, message id.ID) error {
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{
		Type: TypeMediaReleased, SubjectType: "upload", SubjectID: upload,
		Payload: map[string]string{"upload_id": upload.String(), "message_id": message.String()},
	}); err != nil {
		return fmt.Errorf("olay: %w", err)
	}
	return nil
}

// Client is the part of core's ChatEventService the relay uses.
type Client interface {
	ApplyChatEvent(ctx context.Context, in *corev1.ApplyChatEventRequest, opts ...grpc.CallOption) (*corev1.ApplyChatEventResponse, error)
}

// Consumer returns the outbox consumer that delivers Types to core.
func Consumer(c Client) outbox.Consumer {
	return outbox.Consumer{
		Name:  ConsumerName,
		Types: Types,
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := Request(e)
			if err != nil {
				return err
			}
			_, err = c.ApplyChatEvent(ctx, req)
			return err
		},
	}
}

// Request converts a stored event to core's request.
func Request(e outbox.Event) (*corev1.ApplyChatEventRequest, error) {
	if e.Type != TypeMediaReleased {
		return nil, fmt.Errorf("core'a gitmeyen olay türü %q", e.Type)
	}
	var p struct {
		UploadID  string `json:"upload_id"`
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("olay %s: %w", e.ID, err)
	}
	if _, ok := id.Parse(p.UploadID); !ok {
		return nil, fmt.Errorf("olay %s: upload_id", e.ID)
	}
	if _, ok := id.Parse(p.MessageID); !ok {
		return nil, fmt.Errorf("olay %s: message_id", e.ID)
	}
	return &corev1.ApplyChatEventRequest{
		EventId: e.ID.String(), OccurredAt: timestamppb.New(e.OccurredAt),
		Event: &corev1.ApplyChatEventRequest_MediaReleased{MediaReleased: &corev1.ChatMediaReleased{UploadId: p.UploadID, MessageId: p.MessageID}},
	}, nil
}
