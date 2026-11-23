package component

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Personal runs fn in a transaction as user, for data that belongs to the
// person rather than to a tayfa (the member garage, issue #272; ADR-0040
// §4). No tayfa standing is involved: fn's queries must name user as the
// owner of every row they touch. now is the time of the request, UTC.
func (s *Service) Personal(ctx context.Context, user id.ID, fn func(q *store.Queries, now time.Time) error) error {
	now := s.now()
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error { return fn(q, now) })
}
