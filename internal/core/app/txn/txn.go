// Package txn runs core's write transactions.
//
// Every write runs inside WithActor: the transaction first sets
// app.actor_id (SET LOCAL) to the verified caller, so the memberships
// trigger can check the role hierarchy again in the database (ADR-0004).
// Audit records and outbox events are written with Audit and Event in the
// same transaction as the change they describe (ADR-0002); events go
// through internal/platform/outbox.Write.
package txn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// Beginner starts a transaction; *pgxpool.Pool and pgx.Tx satisfy it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithActor runs fn in one transaction in which app.actor_id is actor. The
// transaction commits when fn returns nil and rolls back otherwise; fn's
// error is returned unchanged so callers can match it with errors.Is.
func WithActor(ctx context.Context, db Beginner, actor id.ID, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetActor(ctx, actor.String()); err != nil {
			return fmt.Errorf("işlemi yapan: %w", err)
		}
		return fn(tx, q)
	})
}

// Target is what an audit record or event is about, such as a member
// ("membership", user ID). The zero value means the tayfa itself.
type Target struct {
	Type string
	ID   id.ID
}

func (t Target) pg() (pgtype.Text, pgtype.UUID) {
	if t.Type == "" {
		return pgtype.Text{}, pgtype.UUID{}
	}
	return pgtype.Text{String: t.Type, Valid: true}, t.ID.PG()
}

// Audit appends an audit record (v14 "Denetim"). metadata holds IDs and
// classification only: never message text, location, e-mail, tokens or
// codes. nil means {}.
func Audit(ctx context.Context, q *store.Queries, tayfa, actor id.ID, action string, target Target, metadata map[string]any) error {
	b, err := object(metadata)
	if err != nil {
		return err
	}
	tt, tid := target.pg()
	if err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), ActorID: actor.PG(),
		Action: action, TargetType: tt, TargetID: tid, Metadata: b,
	}); err != nil {
		return fmt.Errorf("denetim kaydı: %w", err)
	}
	return nil
}

// Event writes an outbox event with outbox.Write (ADR-0002), which keeps
// event IDs in commit order for the relay, and records the event's tayfa in
// core's tayfa_id column. q must run in a transaction (WithActor); call
// Event as the last write of it, as outbox.Write asks. The same rule as Audit
// applies to payload: IDs only, no personal data.
func Event(ctx context.Context, q *store.Queries, typ string, tayfa, actor id.ID, subject Target, payload map[string]any) error {
	tx, ok := q.Tx()
	if !ok {
		return errors.New("olay transaction dışında yazılamaz")
	}
	var p any
	if payload != nil {
		p = payload
	}
	eid, err := outbox.Write(ctx, tx, outbox.NewEvent{
		Type: typ, Actor: actor, SubjectType: subject.Type, SubjectID: subject.ID, Payload: p,
	})
	if err != nil {
		return fmt.Errorf("olay: %w", err)
	}
	if tayfa != (id.ID{}) {
		if _, err := q.SetDomainEventTayfa(ctx, store.SetDomainEventTayfaParams{ID: eid.PG(), TayfaID: tayfa.PG()}); err != nil {
			return fmt.Errorf("olayın tayfası: %w", err)
		}
	}
	return nil
}

// activityStep is how often TouchTayfa moves tayfas.last_activity_at at
// most: the inactivity cleanup works in days, and a tayfa row written on
// every RSVP would only add row versions.
const activityStep = time.Hour

// TouchTayfa records activity in a tayfa (tayfas.last_activity_at), the
// clock the inactive tayfa cleanup reads (data model v14 "Tayfa
// yönetimi"). Announcements, event creation, RSVPs and check-ins call it in
// their write transaction. It moves the clock forward only, at most once per
// activityStep, and does not change the tayfa's update time.
func TouchTayfa(ctx context.Context, q *store.Queries, tayfa id.ID, at time.Time) error {
	at = at.UTC()
	if _, err := q.TouchTayfaActivity(ctx, store.TouchTayfaActivityParams{
		ID: tayfa.PG(), At: pgtype.Timestamptz{Time: at, Valid: true},
		NotAfter: pgtype.Timestamptz{Time: at.Add(-activityStep), Valid: true},
	}); err != nil {
		return fmt.Errorf("tayfa aktivitesi: %w", err)
	}
	return nil
}

func object(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return b, nil
}
