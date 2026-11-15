// Package chatevents applies chat's events in core (ChatEventService,
// proto/tayfam/core/v1/chat_events.proto; ADR-0002, ADR-0028). chat's
// outbox relay delivers them with chat's service token (ADR-0013); only
// the tayfam-chat ServiceAccount reaches this code.
//
// The only event is chat.media_released: a message no longer holds its
// photo or attachment, so core deletes the upload if it is still attached
// to that message. Applying it again changes nothing, so no consumer
// bookkeeping is needed and the order of events does not matter.
package chatevents

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MediaReleased is chat's chat.media_released event.
type MediaReleased struct {
	Upload, Message id.ID
}

// Service applies chat's events.
type Service struct {
	q *store.Queries
}

// New returns a Service.
func New(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errors.New("chatevents: eksik yapılandırma")
	}
	return &Service{q: store.New(pool)}, nil
}

// MediaReleased applies one chat.media_released event.
func (s *Service) MediaReleased(ctx context.Context, e MediaReleased) error {
	return media.MessageMediaReleased(ctx, s.q, e.Upload, e.Message)
}
