// Package outbox carries side effects between services without a message
// queue (ADR-0002): a service writes an event to its own domain_events table
// in the same transaction as the change, and a relay in the same service
// delivers it to the target services' idempotent gRPC methods.
//
// The package knows nothing about event types or targets; services supply
// them as Consumers. Every service database has the same two tables
// (domain_events, event_consumers; ADR-0009), so the SQL here is written once
// against their common columns. It is fixed text with bound parameters,
// never assembled from strings.
//
// # Ordering
//
// Consumers keep their position as the last delivered event ID
// (event_consumers.last_event_id) and read forward in ID order. That is only
// safe if no event can commit with an ID below one a consumer has already
// passed. Write guarantees it: it takes a transaction-level advisory lock,
// reads the highest ID and inserts an ID above it (id.After). The lock is
// held until commit, so event-writing transactions commit one at a time in ID
// order. Events must therefore be written with Write, and as the last step of
// the transaction to keep the lock short.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxPayloadBytes matches the domain_events_payload CHECK.
const maxPayloadBytes = 8192

// Same formats as the CHECK constraints on domain_events.
var (
	typePattern        = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)+$`)
	subjectTypePattern = regexp.MustCompile(`^[a-z][a-z_]*$`)
)

// Event is a stored event.
type Event struct {
	ID          id.ID
	Type        string
	Actor       id.ID // zero: none (system job or deleted account)
	SubjectType string
	SubjectID   id.ID // zero when SubjectType is empty
	// Payload is a JSON object. It holds IDs and classification only; no
	// message text, location, e-mail or tokens (ADR-0002).
	Payload    json.RawMessage
	OccurredAt time.Time
}

// NewEvent is an event to write.
type NewEvent struct {
	Type        string
	Actor       id.ID
	SubjectType string
	SubjectID   id.ID
	Payload     any // marshalled to a JSON object; nil means {}
}

const (
	// One lock per database: advisory locks are scoped to the database, so
	// services never contend with each other.
	sqlLock   = `SELECT pg_advisory_xact_lock(hashtextextended('tayfam.outbox.write', 0))`
	sqlLastID = `SELECT id FROM domain_events ORDER BY id DESC LIMIT 1`
	sqlInsert = `INSERT INTO domain_events (id, type, actor_id, subject_type, subject_id, payload)
VALUES ($1, $2, $3, $4, $5, $6)`
)

// Write appends e to domain_events inside tx and returns its ID. Call it as
// the last statement of the transaction (see Ordering).
func Write(ctx context.Context, tx pgx.Tx, e NewEvent) (id.ID, error) {
	if !typePattern.MatchString(e.Type) || len(e.Type) > 64 {
		return id.ID{}, fmt.Errorf("olay türü geçersiz: %q", e.Type)
	}
	if (e.SubjectType == "") != (e.SubjectID == id.ID{}) ||
		(e.SubjectType != "" && (!subjectTypePattern.MatchString(e.SubjectType) || len(e.SubjectType) > 32)) {
		return id.ID{}, errors.New("olay konusu geçersiz")
	}
	payload, err := object(e.Payload)
	if err != nil {
		return id.ID{}, err
	}

	if _, err := tx.Exec(ctx, sqlLock); err != nil {
		return id.ID{}, fmt.Errorf("outbox kilidi: %w", err)
	}
	var last pgtype.UUID
	switch err := tx.QueryRow(ctx, sqlLastID).Scan(&last); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return id.ID{}, fmt.Errorf("son olay: %w", err)
	}
	eid := id.After(id.ID(last.Bytes))
	if _, err := tx.Exec(ctx, sqlInsert, eid.PG(), e.Type, optional(e.Actor), optionalText(e.SubjectType), optional(e.SubjectID), payload); err != nil {
		return id.ID{}, fmt.Errorf("olay yazılamadı: %w", err)
	}
	return eid, nil
}

func object(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("olay içeriği: %w", err)
	}
	if len(b) == 0 || b[0] != '{' {
		return nil, errors.New("olay içeriği JSON nesnesi olmalı")
	}
	if len(b) > maxPayloadBytes {
		return nil, errors.New("olay içeriği çok büyük")
	}
	return b, nil
}

func optional(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}

func optionalText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
