package stats_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

var ctx = context.Background()

const (
	fishType = "test_stat_fish"
	fishDef  = `{"components":[
 {"kind":"measure","key":"distance","labels":{"tr":"Mesafe"},"params":{"unit":"km","scope":"total","entered_by":"organizer","decimals":1}},
 {"kind":"measure","key":"goals","labels":{"tr":"Gol"},"params":{"unit":"goal","scope":"personal","entered_by":"organizer","decimals":0}},
 {"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true,"showcase":false}},
 {"kind":"log","key":"catches","labels":{"tr":"Av"},"params":{"fields":[
  {"key":"species","kind":"text","required":true,"max_length":60,"vocabulary":{"namespace":"fish_species"},"labels":{"tr":"Tür"}},
  {"key":"weight","kind":"decimal","min":0.01,"max":1000,"decimals":2,"unit":"kg","labels":{"tr":"Ağırlık"}}],
  "stat":["distinct:species","sum:weight"]}}]}`
	bloodType = "test_stat_blood"
	bloodDef  = `{"flags":{"sensitive_attendance":true},"components":[
 {"kind":"measure","key":"units","labels":{"tr":"Ünite"},"params":{"unit":"piece","scope":"total","entered_by":"organizer"}}]}`
)

type env struct {
	svc    *stats.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fw, err := component.New(component.Config{Pool: d.App, Log: log})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	e := &env{svc: stats.New(fw), tayfas: tayfas, pool: d.App, admin: d.Admin}
	// The tests bring their own badges: the seed's (issue #260) are removed.
	e.exec(t, `DELETE FROM catalog_badges`)
	for key, def := range map[string]string{fishType: fishDef, bloodType: bloodDef} {
		e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
			VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
		e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
			SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
	}
	return e
}

// exec runs a fixture statement as a superuser with triggers off.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "İstatistik Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

func (e *env) member(t *testing.T, tf id.ID) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: authz.Member.String(), Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

// event writes an event of typ from start for an hour, and the verified
// attendance of who.
func (e *env) event(t *testing.T, tf id.ID, typ string, start time.Time, who ...id.ID) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, $3, 1, 'online', 'Buluşma', $4, $5, 'https://example.test/oda')`, ev.PG(), tf.PG(), typ, start, start.Add(time.Hour))
	for _, u := range who {
		e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
			VALUES ($1, $2, 'going', 'attended', 'location', $3)`, ev.PG(), u.PG(), start)
	}
	return ev
}

func (e *env) measure(t *testing.T, ev id.ID, key string, user id.ID, v float64) {
	t.Helper()
	u := pgtype.UUID{}
	if user != (id.ID{}) {
		u = user.PG()
	}
	e.exec(t, `INSERT INTO event_measurements (id, event_id, component_key, user_id, value) VALUES ($1, $2, $3, $4, $5)`,
		id.New().PG(), ev.PG(), key, u, v)
}

func (e *env) word(t *testing.T, tf id.ID, text string) id.ID {
	t.Helper()
	v := id.New()
	e.exec(t, `INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized) VALUES ($1, $2, 'fish_species', $3, $4)`,
		v.PG(), tf.PG(), text, vocabulary.Normalize(text))
	return v
}

func (e *env) catch(t *testing.T, ev, user, species id.ID, weight float64) {
	t.Helper()
	entry := id.New()
	values, err := json.Marshal(map[string]float64{"weight": weight})
	must(t, err)
	e.exec(t, `INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, client_request_id) VALUES ($1, $2, 'catches', $3, $4, $5)`,
		entry.PG(), ev.PG(), user.PG(), values, id.New().PG())
	e.exec(t, `INSERT INTO event_log_entry_vocabulary (entry_id, field_key, vocabulary_id) VALUES ($1, 'species', $2)`, entry.PG(), species.PG())
}

func (e *env) recompute(t *testing.T, tf id.ID, now time.Time) {
	t.Helper()
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		badges, err := badge.Usable(ctx, q)
		if err != nil {
			return err
		}
		return stats.Recompute(ctx, q, tf, now, badges)
	}))
}

// tayfaStat returns a tayfa total, or -1 when there is no row.
func (e *env) tayfaStat(t *testing.T, tf id.ID, year int32, metric string) float64 {
	t.Helper()
	var v float64
	err := e.pool.QueryRow(ctx, `SELECT value::float8 FROM tayfa_stats WHERE tayfa_id = $1 AND year = $2 AND metric_key = $3`, tf.PG(), year, metric).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return -1
	}
	must(t, err)
	return v
}

func (e *env) userStat(t *testing.T, u id.ID, year int32, metric string) float64 {
	t.Helper()
	var v float64
	err := e.pool.QueryRow(ctx, `SELECT value::float8 FROM user_stats WHERE user_id = $1 AND year = $2 AND metric_key = $3`, u.PG(), year, metric).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return -1
	}
	must(t, err)
	return v
}

// TestRecompute: only frozen events count; the year is Istanbul's; nothing
// sensitive is stored; a vocabulary merge and a deleted event change the
// numbers on the next computation; the same computation twice writes the
// same rows; the threshold and first badges are given, the tayfa's without
// the sensitive event.
func TestRecompute(t *testing.T) {
	e := setup(t)
	for i, b := range []struct{ key, scope, rule string }{
		{"test_attend_one", "user", `{"kind": "threshold", "metric": "attended", "value": 1, "period": "all_time"}`},
		{"test_first_fish", "user", `{"kind": "first", "event_type": "` + fishType + `"}`},
		{"test_tayfa_two", "tayfa", `{"kind": "threshold", "metric": "events", "value": 2, "period": "all_time"}`},
		{"test_tayfa_three", "tayfa", `{"kind": "threshold", "metric": "events", "value": 3, "period": "all_time"}`},
	} {
		_, err := e.admin.Exec(ctx, `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ($1, $2, $3, '{"tr": "Rozet"}', 'crown', $4::jsonb)`,
			b.key, b.scope, i+1, b.rule)
		must(t, err)
	}
	tf, _ := e.newTayfa(t)
	other, _ := e.newTayfa(t)
	a, b := e.member(t, tf), e.member(t, tf)
	now := time.Now().UTC().Truncate(time.Microsecond)

	ev1 := e.event(t, tf, fishType, time.Date(2025, 6, 1, 7, 0, 0, 0, time.UTC), a, b)
	// 2025-12-31 21:30 UTC is 2026-01-01 00:30 in Istanbul.
	ev2 := e.event(t, tf, fishType, time.Date(2025, 12, 31, 21, 30, 0, 0, time.UTC), a)
	e.event(t, tf, bloodType, time.Date(2025, 3, 1, 7, 0, 0, 0, time.UTC), a)
	open := e.event(t, tf, fishType, now.Add(-component.FreezeAfter).Add(-30*time.Minute), a)
	e.event(t, other, fishType, time.Date(2025, 6, 1, 7, 0, 0, 0, time.UTC), a)
	e.measure(t, ev1, "distance", id.ID{}, 10)
	e.measure(t, ev1, "goals", a, 2)
	e.measure(t, ev1, "net", a, 50)
	e.measure(t, open, "distance", id.ID{}, 100)
	levrek, lavrak := e.word(t, tf, "Levrek"), e.word(t, tf, "lavrak")
	e.catch(t, ev1, a, levrek, 1)
	e.catch(t, ev1, a, lavrak, 2)

	e.recompute(t, tf, now)
	for _, tc := range []struct {
		year   int32
		metric string
		want   float64
	}{
		{2025, "events", 2},
		{2026, "events", 1},
		{0, "events", 3},
		{0, "attended", 4},
		{0, "participants", 2},
		{0, "duration", 120},
		{0, fishType + ".distance", 10},
		{0, fishType + ".goals", 2},
		{2025, fishType + ".catches.distinct_species", 2},
		{2025, fishType + ".catches.sum_weight", 3},
		{2025, fishType + ".catches.count", 2},
		{0, bloodType + ".events", 1},
		{0, bloodType + ".attended", 1},
		{0, fishType + ".net", -1},
		{0, bloodType + ".participants", -1},
	} {
		if got := e.tayfaStat(t, tf, tc.year, tc.metric); got != tc.want {
			t.Errorf("tayfa %d %s = %v, %v bekleniyordu", tc.year, tc.metric, got, tc.want)
		}
	}
	if got := e.userStat(t, a, 0, "attended"); got != 2 {
		t.Errorf("kişinin katılımı %v (hassas ya da donmamış etkinlik sayıldı)", got)
	}
	if got := e.userStat(t, a, 0, fishType+".goals"); got != 2 {
		t.Errorf("kişinin ölçümü %v", got)
	}
	if n := e.count(t, `SELECT count(*) FROM user_stats WHERE metric_key LIKE $1 OR metric_key = $2`, bloodType+".%", fishType+".net"); n != 0 {
		t.Errorf("hassas veri kişisel istatistikte: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM tayfa_stats WHERE tayfa_id = $1`, other.PG()); n != 0 {
		t.Errorf("başka tayfa hesaplandı: %d", n)
	}

	// Badges: the person's attended and first; the tayfa's two-event badge
	// (ev1, ev2) but not the three-event one, which needs the sensitive
	// event.
	userBadges := func(u id.ID) int {
		return e.count(t, `SELECT count(*) FROM user_badges WHERE user_id = $1 AND badge_key IN ('test_attend_one', 'test_first_fish')`, u.PG())
	}
	if userBadges(a) != 2 || userBadges(b) != 2 {
		t.Errorf("kişi rozetleri: %d %d", userBadges(a), userBadges(b))
	}
	if n := e.count(t, `SELECT count(*) FROM tayfa_badges WHERE tayfa_id = $1 AND badge_key = 'test_tayfa_two'`, tf.PG()); n != 1 {
		t.Errorf("tayfa rozeti: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM tayfa_badges WHERE tayfa_id = $1 AND badge_key = 'test_tayfa_three'`, tf.PG()); n != 0 {
		t.Error("hassas etkinlik tayfa rozetine sayıldı")
	}

	// The same computation writes the same rows and no new badge.
	snapshot := func() string {
		var s string
		must(t, e.pool.QueryRow(ctx, `SELECT concat_ws('|',
			(SELECT string_agg(concat_ws(':', year, metric_key, source, value, event_count), ',' ORDER BY year, metric_key) FROM tayfa_stats WHERE tayfa_id = $1),
			(SELECT string_agg(concat_ws(':', user_id, year, metric_key, source, value, event_count), ',' ORDER BY user_id, year, metric_key) FROM user_stats WHERE tayfa_id = $1),
			(SELECT count(*) FROM user_badges), (SELECT count(*) FROM tayfa_badges))`, tf.PG()).Scan(&s))
		return s
	}
	before := snapshot()
	e.recompute(t, tf, now)
	if after := snapshot(); after != before {
		t.Fatalf("tekrar farklı yazdı:\n%s\n%s", before, after)
	}

	// The open event freezes a minute later.
	e.recompute(t, tf, now.Add(31*time.Minute))
	if got := e.tayfaStat(t, tf, 0, fishType+".distance"); got != 110 {
		t.Errorf("donan etkinliğin ölçümü: %v", got)
	}
	// Three events that are not sensitive now.
	if n := e.count(t, `SELECT count(*) FROM tayfa_badges WHERE tayfa_id = $1 AND badge_key = 'test_tayfa_three'`, tf.PG()); n != 1 {
		t.Errorf("üç etkinlik rozeti: %d", n)
	}

	// A merge counts the target; a deleted event drops out.
	e.exec(t, `UPDATE tayfa_vocabulary SET merged_into = $1 WHERE id = $2`, levrek.PG(), lavrak.PG())
	e.exec(t, `UPDATE events SET deleted_at = now() WHERE id = $1`, ev2.PG())
	e.recompute(t, tf, now.Add(31*time.Minute))
	if got := e.tayfaStat(t, tf, 2025, fishType+".catches.distinct_species"); got != 1 {
		t.Errorf("birleştirmeden sonra farklı tür: %v", got)
	}
	// ev1, the blood drive and the event that froze; ev2 is gone.
	if got := e.tayfaStat(t, tf, 0, "events"); got != 3 {
		t.Errorf("silinen etkinlik düşmedi: %v", got)
	}
	// Badges stay.
	if n := e.count(t, `SELECT count(*) FROM tayfa_badges WHERE tayfa_id = $1`, tf.PG()); n != 2 {
		t.Errorf("rozet geri alındı: %d", n)
	}
}

// TestTayfaView: members read the totals and the active members'
// contributions; anyone else is refused; a bad year is a validation error.
func TestTayfaView(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a, b := e.member(t, tf), e.member(t, tf)
	outsider := id.New()
	now := time.Now().UTC()
	ev := e.event(t, tf, fishType, time.Date(2025, 6, 1, 7, 0, 0, 0, time.UTC), a, b)
	e.measure(t, ev, "goals", a, 3)
	e.measure(t, ev, "goals", b, 1)
	e.measure(t, ev, "net", a, 70)

	v, err := e.svc.Tayfa(ctx, b, tf, stats.AllTime)
	must(t, err)
	if len(v.Totals) != 0 || len(v.Years) != 0 || !v.ComputedAt.IsZero() {
		t.Fatalf("hesaplanmadan: %+v", v)
	}
	e.recompute(t, tf, now)

	v, err = e.svc.Tayfa(ctx, b, tf, stats.AllTime)
	must(t, err)
	if len(v.Years) != 1 || v.Years[0] != 2025 || v.ComputedAt.IsZero() || len(v.Totals) == 0 {
		t.Fatalf("görünüm: %+v", v)
	}
	contrib := map[id.ID]float64{}
	for _, c := range v.Contributions {
		if c.Metric == fishType+".net" {
			t.Fatal("hassas ölçüm katkılarda")
		}
		if c.Metric == fishType+".goals" {
			contrib[c.User] = c.Value
		}
		if c.Metric == "attended" || c.Metric == "duration" {
			t.Fatalf("ortak metrik katkılarda: %+v", c)
		}
	}
	if contrib[a] != 3 || contrib[b] != 1 || len(contrib) != 2 {
		t.Fatalf("katkılar: %v", contrib)
	}
	v, err = e.svc.Tayfa(ctx, admin, tf, 2025)
	must(t, err)
	if v.Year != 2025 || len(v.Totals) == 0 {
		t.Fatalf("yıl: %+v", v)
	}

	// One who left is not in the contributions.
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), a.PG())
	v, err = e.svc.Tayfa(ctx, b, tf, stats.AllTime)
	must(t, err)
	for _, c := range v.Contributions {
		if c.User == a {
			t.Fatal("ayrılanın katkısı listede")
		}
	}

	if _, err := e.svc.Tayfa(ctx, outsider, tf, stats.AllTime); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("üye olmayan: %v", err)
	}
	if _, err := e.svc.Tayfa(ctx, a, tf, stats.AllTime); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("ayrılan: %v", err)
	}
	var verr *apperr.ValidationError
	if _, err := e.svc.Tayfa(ctx, b, tf, 1999); !errors.As(err, &verr) || verr.Field != "year" {
		t.Errorf("geçersiz yıl: %v", err)
	}

	// The readers for the showcase and the profile.
	q := store.New(e.pool)
	totals, at, err := stats.TayfaTotals(ctx, q, tf, stats.AllTime)
	must(t, err)
	if at.IsZero() || len(totals) == 0 {
		t.Fatalf("TayfaTotals: %v %v", totals, at)
	}
	mine, err := stats.UserTotals(ctx, q, b, stats.AllTime)
	must(t, err)
	found := false
	for _, s := range mine {
		if s.Metric == "attended" && s.Value == 1 && s.Source == stats.SourceCommon {
			found = true
		}
	}
	if !found {
		t.Fatalf("UserTotals: %+v", mine)
	}
}

// TestQueueEvent: the vocabulary consumer queues the event's tayfa once;
// an unknown tayfa or a malformed event is skipped without an error.
func TestQueueEvent(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	q := store.New(e.pool)
	now := time.Now().UTC()
	payload := func(v string) json.RawMessage {
		b, err := json.Marshal(map[string]any{"tayfa_id": v, "years": []int{2025}})
		must(t, err)
		return b
	}
	for range 2 {
		must(t, stats.QueueEvent(ctx, q, outbox.Event{ID: id.New(), Type: vocabulary.TypeMerged, Payload: payload(tf.String())}, now))
	}
	must(t, stats.QueueEvent(ctx, q, outbox.Event{ID: id.New(), Type: vocabulary.TypeMerged, Payload: payload(id.New().String())}, now))
	must(t, stats.QueueEvent(ctx, q, outbox.Event{ID: id.New(), Type: vocabulary.TypeUnmerged, Payload: payload("x")}, now))
	must(t, stats.QueueEvent(ctx, q, outbox.Event{ID: id.New(), Type: vocabulary.TypeUnmerged, Payload: json.RawMessage(`[`)}, now))
	if n := e.count(t, `SELECT count(*) FROM tayfa_stat_queue`); n != 1 {
		t.Fatalf("kuyruk: %d", n)
	}
	c := stats.Consumer(e.pool, time.Now)
	if c.Name != stats.ConsumerName || len(c.Types) != 2 {
		t.Fatalf("tüketici: %+v", c)
	}
}
