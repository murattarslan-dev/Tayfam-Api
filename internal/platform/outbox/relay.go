package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// consumerNamePattern matches the event_consumers_name_format CHECK.
var consumerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)

// Consumer is one target of the relay, such as core receiving identity's
// user events. Each consumer has its own position and is delivered to
// independently: a target that is down does not hold up the others.
type Consumer struct {
	// Name is the consumer's key in event_consumers, e.g. "core".
	Name string
	// Types are the event types this consumer receives. Other events are
	// not read for it.
	Types []string
	// Deliver sends one event. It must be safe to repeat: the relay
	// delivers at least once, and the target method is idempotent
	// (ADR-0002). An error is retried with backoff; the consumer does not
	// move past an event until it is delivered, so events reach the target
	// in order.
	Deliver func(ctx context.Context, e Event) error
}

// RelayConfig configures a Relay.
type RelayConfig struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Consumers []Consumer
	// Optional; defaults in parentheses.
	PollInterval time.Duration // wait when there is nothing to deliver (2 s)
	MinBackoff   time.Duration // first wait after a failure (1 s)
	MaxBackoff   time.Duration // longest wait between attempts (5 min)
	BatchSize    int           // events read at once (100)
}

// Relay delivers events from domain_events to consumers.
type Relay struct {
	cfg RelayConfig
}

// NewRelay validates cfg and returns a Relay.
func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.Pool == nil || cfg.Log == nil || len(cfg.Consumers) == 0 {
		return nil, errors.New("outbox: havuz, log ve en az bir tüketici zorunlu")
	}
	names := map[string]bool{}
	for _, c := range cfg.Consumers {
		if !consumerNamePattern.MatchString(c.Name) || names[c.Name] {
			return nil, fmt.Errorf("outbox: tüketici adı geçersiz ya da tekrar ediyor: %q", c.Name)
		}
		names[c.Name] = true
		if len(c.Types) == 0 || c.Deliver == nil {
			return nil, fmt.Errorf("outbox: %s için olay türü ve teslim fonksiyonu zorunlu", c.Name)
		}
		for _, t := range c.Types {
			if !typePattern.MatchString(t) {
				return nil, fmt.Errorf("outbox: olay türü geçersiz: %q", t)
			}
		}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = max(5*time.Minute, cfg.MinBackoff)
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	return &Relay{cfg: cfg}, nil
}

// Run delivers events until ctx is done, then returns nil. Every consumer
// runs in its own goroutine, all ending with ctx.
func (r *Relay) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, c := range r.cfg.Consumers {
		wg.Go(func() { r.runConsumer(ctx, c) })
	}
	wg.Wait()
	return nil
}

func (r *Relay) runConsumer(ctx context.Context, c Consumer) {
	log := r.cfg.Log.With(slog.String("consumer", c.Name))
	backoff := r.cfg.MinBackoff
	for ctx.Err() == nil {
		n, err := r.Step(ctx, c)
		var wait time.Duration
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Only IDs, types and the error; never the payload.
			log.WarnContext(ctx, "olay teslim edilemedi, tekrar denenecek",
				slog.String("hata", err.Error()), slog.Duration("bekleme", backoff))
			wait = backoff
			backoff = min(backoff*2, r.cfg.MaxBackoff)
		case n == 0:
			backoff = r.cfg.MinBackoff
			wait = r.cfg.PollInterval
		default:
			backoff = r.cfg.MinBackoff
			continue
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

const (
	sqlEnsureConsumer = `INSERT INTO event_consumers (consumer_name) VALUES ($1) ON CONFLICT (consumer_name) DO NOTHING`
	sqlPosition       = `SELECT last_event_id FROM event_consumers WHERE consumer_name = $1`
	sqlNext           = `SELECT id, type, actor_id, subject_type, subject_id, payload, occurred_at
FROM domain_events
WHERE ($1::uuid IS NULL OR id > $1) AND type = ANY($2::text[])
ORDER BY id
LIMIT $3`
	// The position only moves forward, even if two relays run at once
	// during a rolling update.
	sqlAdvance = `UPDATE event_consumers SET last_event_id = $2
WHERE consumer_name = $1 AND (last_event_id IS NULL OR last_event_id < $2)`
)

// Step delivers the next batch of events for c, stopping at the first
// failure, and returns how many were delivered. Run calls it in a loop;
// tests call it directly.
func (r *Relay) Step(ctx context.Context, c Consumer) (int, error) {
	pos, err := r.position(ctx, c.Name)
	if err != nil {
		return 0, err
	}
	events, err := r.next(ctx, pos, c.Types)
	if err != nil {
		return 0, err
	}
	for i, e := range events {
		if err := c.Deliver(ctx, e); err != nil {
			return i, fmt.Errorf("olay %s (%s): %w", e.ID, e.Type, err)
		}
		if _, err := r.cfg.Pool.Exec(ctx, sqlAdvance, c.Name, e.ID.PG()); err != nil {
			return i, fmt.Errorf("tüketici konumu: %w", err)
		}
	}
	return len(events), nil
}

func (r *Relay) position(ctx context.Context, name string) (pgtype.UUID, error) {
	if _, err := r.cfg.Pool.Exec(ctx, sqlEnsureConsumer, name); err != nil {
		return pgtype.UUID{}, fmt.Errorf("tüketici kaydı: %w", err)
	}
	var pos pgtype.UUID
	if err := r.cfg.Pool.QueryRow(ctx, sqlPosition, name).Scan(&pos); err != nil {
		return pgtype.UUID{}, fmt.Errorf("tüketici konumu: %w", err)
	}
	return pos, nil
}

func (r *Relay) next(ctx context.Context, after pgtype.UUID, types []string) ([]Event, error) {
	rows, err := r.cfg.Pool.Query(ctx, sqlNext, after, types, r.cfg.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("olaylar okunamadı: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var (
			eid, actor, subject pgtype.UUID
			subjectType         pgtype.Text
			e                   Event
		)
		if err := row.Scan(&eid, &e.Type, &actor, &subjectType, &subject, &e.Payload, &e.OccurredAt); err != nil {
			return Event{}, err
		}
		e.ID = id.ID(eid.Bytes)
		if actor.Valid {
			e.Actor = id.ID(actor.Bytes)
		}
		if subjectType.Valid && subject.Valid {
			e.SubjectType, e.SubjectID = subjectType.String, id.ID(subject.Bytes)
		}
		e.OccurredAt = e.OccurredAt.UTC()
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("olaylar okunamadı: %w", err)
	}
	return events, nil
}
