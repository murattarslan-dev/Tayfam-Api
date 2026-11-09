package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// schema is the outbox part every service database has (ADR-0009); the
// services' own migrations create the same tables.
const schema = `-- +goose Up
CREATE TABLE domain_events (
  id           uuid PRIMARY KEY,
  type         text NOT NULL,
  actor_id     uuid,
  subject_type text,
  subject_id   uuid,
  payload      jsonb NOT NULL DEFAULT '{}',
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT domain_events_type_format CHECK (type ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND char_length(type) <= 64),
  CONSTRAINT domain_events_subject CHECK ((subject_type IS NULL) = (subject_id IS NULL)),
  CONSTRAINT domain_events_payload CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 8192)
);
CREATE TABLE event_consumers (
  consumer_name text PRIMARY KEY,
  last_event_id uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT event_consumers_name_format CHECK (consumer_name ~ '^[a-z][a-z0-9_.-]{0,62}$')
);
-- The services' runtime roles get exactly this on the outbox tables
-- (db/<service>/migrations/*_app_role.sql, ADR-0014).
GRANT SELECT, INSERT ON domain_events TO outbox_app;
GRANT SELECT, INSERT, UPDATE ON event_consumers TO outbox_app;
-- +goose Down
DROP TABLE event_consumers;
DROP TABLE domain_events;
`

func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.New(t, "outbox", fstest.MapFS{"00001_outbox.sql": {Data: []byte(schema)}})
}

func write(t *testing.T, pool *pgxpool.Pool, e NewEvent) id.ID {
	t.Helper()
	var eid id.ID
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		eid, err = Write(context.Background(), tx, e)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return eid
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// collector records delivered events.
type collector struct {
	mu   sync.Mutex
	got  []Event
	fail int // fail the next n deliveries
}

func (c *collector) deliver(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail > 0 {
		c.fail--
		return errors.New("hedef kapalı")
	}
	c.got = append(c.got, e)
	return nil
}

func (c *collector) ids() []id.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]id.ID, len(c.got))
	for i, e := range c.got {
		out[i] = e.ID
	}
	return out
}

func TestWriteValidates(t *testing.T) {
	pool := newDB(t)
	subject := id.New()
	for name, e := range map[string]NewEvent{
		"tür biçimi":           {Type: "Kullanici"},
		"konu türü yok":        {Type: "user.deleted", SubjectID: subject},
		"konu kimliği yok":     {Type: "user.deleted", SubjectType: "user"},
		"konu türü biçimi":     {Type: "user.deleted", SubjectType: "User", SubjectID: subject},
		"nesne olmayan içerik": {Type: "user.deleted", Payload: []int{1}},
		"çok büyük içerik":     {Type: "user.deleted", Payload: map[string]string{"x": string(make([]byte, 9000))}},
	} {
		t.Run(name, func(t *testing.T) {
			err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
				_, err := Write(context.Background(), tx, e)
				return err
			})
			if err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}

func TestWriteAndDeliver(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	user, actor := id.New(), id.New()
	e1 := write(t, pool, NewEvent{Type: "user.profile_updated", Actor: actor, SubjectType: "user", SubjectID: user, Payload: map[string]string{"display_name": "Deniz"}})
	write(t, pool, NewEvent{Type: "device.removed"})
	e3 := write(t, pool, NewEvent{Type: "user.deleted", SubjectType: "user", SubjectID: user})

	col := &collector{}
	r, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), Consumers: []Consumer{{Name: "core", Types: []string{"user.profile_updated", "user.deleted"}, Deliver: col.deliver}}})
	if err != nil {
		t.Fatal(err)
	}
	c := r.cfg.Consumers[0]
	if n, err := r.Step(ctx, c); err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if n, err := r.Step(ctx, c); err != nil || n != 0 {
		t.Fatalf("ikinci adımda tekrar teslim: n = %d, err = %v", n, err)
	}
	got := col.got
	if len(got) != 2 || got[0].ID != e1 || got[1].ID != e3 {
		t.Fatalf("teslim edilen = %+v", got)
	}
	if got[0].Actor != actor || got[0].SubjectType != "user" || got[0].SubjectID != user || got[0].OccurredAt.IsZero() {
		t.Fatalf("olay alanları = %+v", got[0])
	}
	var p map[string]string
	if err := json.Unmarshal(got[0].Payload, &p); err != nil || p["display_name"] != "Deniz" {
		t.Fatalf("içerik = %s", got[0].Payload)
	}
	if got[1].Actor != (id.ID{}) {
		t.Fatal("aktörsüz olayın aktörü var")
	}
}

func TestRelayRetriesWithoutSkipping(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	e1 := write(t, pool, NewEvent{Type: "user.deleted", SubjectType: "user", SubjectID: id.New()})
	e2 := write(t, pool, NewEvent{Type: "user.deleted", SubjectType: "user", SubjectID: id.New()})
	col := &collector{fail: 2}
	r, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), Consumers: []Consumer{{Name: "core", Types: []string{"user.deleted"}, Deliver: col.deliver}}})
	if err != nil {
		t.Fatal(err)
	}
	c := r.cfg.Consumers[0]
	for range 2 {
		if _, err := r.Step(ctx, c); err == nil {
			t.Fatal("hata bekleniyordu")
		}
	}
	if n, err := r.Step(ctx, c); err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if ids := col.ids(); len(ids) != 2 || ids[0] != e1 || ids[1] != e2 {
		t.Fatalf("sıra = %v", ids)
	}
}

func TestConsumersAreIndependent(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	write(t, pool, NewEvent{Type: "user.deleted", SubjectType: "user", SubjectID: id.New()})
	down, up := &collector{fail: 100}, &collector{}
	r, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), Consumers: []Consumer{
		{Name: "core", Types: []string{"user.deleted"}, Deliver: down.deliver},
		{Name: "chat", Types: []string{"user.deleted"}, Deliver: up.deliver},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Step(ctx, r.cfg.Consumers[0]); err == nil {
		t.Fatal("core kapalı olmalıydı")
	}
	if n, err := r.Step(ctx, r.cfg.Consumers[1]); err != nil || n != 1 {
		t.Fatalf("chat: n = %d, err = %v", n, err)
	}
}

// Writers that commit out of the order they started in must not let a
// consumer skip an event (see Ordering in the package doc).
func TestConcurrentWritersAreNotSkipped(t *testing.T) {
	pool := newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col := &collector{}
	r, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), PollInterval: time.Millisecond,
		Consumers: []Consumer{{Name: "core", Types: []string{"user.deleted"}, Deliver: col.deliver}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()

	const writers = 20
	var wg sync.WaitGroup
	var written sync.Map
	var errs atomic.Int32
	for range writers {
		wg.Go(func() {
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				eid, err := Write(ctx, tx, NewEvent{Type: "user.deleted", SubjectType: "user", SubjectID: id.New()})
				if err != nil {
					return err
				}
				written.Store(eid, true)
				time.Sleep(time.Duration(mrand.IntN(20)) * time.Millisecond) // #nosec G404 -- test timing
				return nil
			})
			if err != nil {
				errs.Add(1)
			}
		})
	}
	wg.Wait()
	if errs.Load() != 0 {
		t.Fatal("yazma hatası")
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(col.ids()) < writers && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	ids := col.ids()
	if len(ids) != writers {
		t.Fatalf("%d olaydan %d tanesi teslim edildi", writers, len(ids))
	}
	for i, eid := range ids {
		if _, ok := written.Load(eid); !ok {
			t.Fatalf("yazılmamış olay teslim edildi: %s", eid)
		}
		if i > 0 && ids[i-1].String() >= eid.String() {
			t.Fatal("teslim sırası bozuk")
		}
	}
}

func TestRunStopsWithContext(t *testing.T) {
	pool := newDB(t)
	r, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), Consumers: []Consumer{{Name: "core", Types: []string{"user.deleted"}, Deliver: (&collector{}).deliver}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay durmadı")
	}
}

func TestNewRelayConfig(t *testing.T) {
	pool := newDB(t)
	deliver := (&collector{}).deliver
	for name, cs := range map[string][]Consumer{
		"tüketici yok": nil,
		"ad biçimi":    {{Name: "Core", Types: []string{"user.deleted"}, Deliver: deliver}},
		"ad tekrarı":   {{Name: "core", Types: []string{"user.deleted"}, Deliver: deliver}, {Name: "core", Types: []string{"user.deleted"}, Deliver: deliver}},
		"tür yok":      {{Name: "core", Deliver: deliver}},
		"tür biçimi":   {{Name: "core", Types: []string{"deleted"}, Deliver: deliver}},
		"teslim yok":   {{Name: "core", Types: []string{"user.deleted"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRelay(RelayConfig{Pool: pool, Log: discard(), Consumers: cs}); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}
