package badge_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// testBadges is the smallest set the tests need; it replaces the seed's
// list (issue #260), so that only these badges are earned.
const testBadges = `DELETE FROM catalog_badges;
INSERT INTO catalog_badges (key, scope, position, labels, icon, rule, status) VALUES
	('first_event', 'user', 1, '{"tr": "İlk etkinlik"}', 'flag', '{"kind": "first"}', 'active'),
	('first_hike', 'user', 2, '{"tr": "İlk yürüyüş"}', 'hiking', '{"kind": "first", "event_type": "hike"}', 'active'),
	('ten_events', 'user', 3, '{"tr": "On etkinlik"}', 'medal', '{"kind": "threshold", "metric": "attended", "value": 10, "period": "all_time"}', 'active'),
	('yearly_hours', 'user', 4, '{"tr": "Yılın saatleri"}', 'clock', '{"kind": "threshold", "metric": "duration", "value": 600, "period": "year"}', 'active'),
	('tayfa_fifty', 'tayfa', 5, '{"tr": "Elli etkinlik"}', 'trophy', '{"kind": "threshold", "metric": "events", "value": 50, "period": "year"}', 'active'),
	('champion', 'user', 6, '{"tr": "Şampiyon"}', 'crown', '{"kind": "champion"}', 'active'),
	('cup_champion', 'user', 7, '{"tr": "Kupa şampiyonu"}', 'cup', '{"kind": "champion", "event_type": "test_badge_cup"}', 'active'),
	('race_champion', 'user', 8, '{"tr": "Yarış şampiyonu"}', 'cup', '{"kind": "champion", "event_type": "test_badge_race"}', 'active'),
	('old_champion', 'user', 9, '{"tr": "Eski şampiyon"}', 'crown', '{"kind": "champion"}', 'retired')`

// Test types: cupType has a celebration with winners chosen by hand,
// raceType too (another type), bloodType's attendance is sensitive.
const (
	cupType   = "test_badge_cup"
	raceType  = "test_badge_race"
	bloodType = "test_badge_blood"
	plainDef  = `{"components":[{"kind":"celebration"}]}`
	bloodDef  = `{"components":[{"kind":"celebration"}],"flags":{"sensitive_attendance":true}}`
)

type env struct {
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	tayfas *tayfa.Service
	now    time.Time
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
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	e := &env{pool: d.App, admin: d.Admin, tayfas: tayfas, now: time.Now().UTC().Truncate(time.Microsecond)}
	for key, def := range map[string]string{cupType: plainDef, raceType: plainDef, bloodType: bloodDef} {
		e.seed(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
			VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
		e.seed(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
			SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
	}
	_, err = d.Admin.Exec(ctx, testBadges)
	must(t, err)
	return e
}

// seed runs a fixture statement as a superuser with triggers off.
func (e *env) seed(t *testing.T, sql string, args ...any) {
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

// tx runs fn as the service does: one transaction as core_app.
func (e *env) tx(t *testing.T, fn func(q *store.Queries) error) {
	t.Helper()
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) }))
}

func (e *env) usable(t *testing.T) []catalog.Badge {
	t.Helper()
	b, err := badge.Usable(ctx, store.New(e.pool))
	must(t, err)
	return b
}

type notice struct {
	Type       string   `json:"type"`
	TayfaID    string   `json:"tayfa_id"`
	TargetType string   `json:"target_type"`
	TargetID   string   `json:"target_id"`
	Recipients []string `json:"recipient_ids"`
}

// notices returns the badge_earned requests in order.
func (e *env) notices(t *testing.T) []notice {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT payload FROM domain_events WHERE type = $1 ORDER BY id`, notify.EventType)
	must(t, err)
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var n notice
		must(t, json.Unmarshal(raw, &n))
		if n.Type == notify.TypeBadgeEarned {
			out = append(out, n)
		}
	}
	must(t, rows.Err())
	return out
}

func keys(bs []badge.Earned) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Key)
	}
	return out
}

func TestProfileVisible(t *testing.T) {
	for _, c := range []struct{ self, stats, want bool }{
		{true, false, true}, {true, true, true}, {false, true, true}, {false, false, false},
	} {
		if got := badge.ProfileVisible(c.self, c.stats); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// Usable skips retired definitions and any rule that leans on sensitive
// data, even when the database holds it (ADR-0011).
func TestUsable(t *testing.T) {
	e := setup(t)
	_, err := e.admin.Exec(ctx, `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES
		('blood_first', 'user', 20, '{"tr": "x"}', 'drop', '{"kind": "first", "event_type": "test_badge_blood"}'),
		('blood_units', 'tayfa', 21, '{"tr": "x"}', 'drop', '{"kind": "threshold", "metric": "test_badge_blood.attended", "value": 1, "period": "all_time"}')`)
	must(t, err)
	var got []string
	for _, b := range e.usable(t) {
		got = append(got, b.Key)
	}
	want := []string{"first_event", "first_hike", "ten_events", "yearly_hours", "tayfa_fifty", "champion", "cup_champion", "race_champion"}
	if !slices.Equal(got, want) {
		t.Fatalf("kullanılabilir = %v", got)
	}
}

func TestDue(t *testing.T) {
	e := setup(t)
	bs := e.usable(t)
	for name, c := range map[string]struct {
		totals badge.Totals
		want   []badge.Award
	}{
		"hiç": {badge.Totals{Scope: catalog.BadgeScopeUser}, nil},
		"ilk etkinlik": {
			badge.Totals{Scope: catalog.BadgeScopeUser, AllTime: map[string]float64{"attended": 1, "camp.attended": 1}},
			[]badge.Award{{Key: "first_event"}},
		},
		"ilk yürüyüş ve on etkinlik": {
			badge.Totals{Scope: catalog.BadgeScopeUser, AllTime: map[string]float64{"attended": 10, "hike.attended": 2}},
			[]badge.Award{{Key: "first_event"}, {Key: "first_hike"}, {Key: "ten_events"}},
		},
		"yıllık eşik yılla": {
			badge.Totals{Scope: catalog.BadgeScopeUser, AllTime: map[string]float64{"attended": 3, "duration": 900}, Year: 2026, YearTotals: map[string]float64{"duration": 600}},
			[]badge.Award{{Key: "first_event"}, {Key: "yearly_hours", Year: 2026}},
		},
		// Every time is all time; a yearly badge needs its year.
		"yılsız yıllık eşik": {
			badge.Totals{Scope: catalog.BadgeScopeUser, AllTime: map[string]float64{"duration": 900}},
			nil,
		},
		"eşiğin altı": {
			badge.Totals{Scope: catalog.BadgeScopeUser, AllTime: map[string]float64{"attended": 9.99}, Year: 2026, YearTotals: map[string]float64{"duration": 599}},
			[]badge.Award{{Key: "first_event"}},
		},
		"tayfa": {
			badge.Totals{Scope: catalog.BadgeScopeTayfa, AllTime: map[string]float64{"attended": 500}, Year: 2027, YearTotals: map[string]float64{"events": 50, "attended": 400}},
			[]badge.Award{{Key: "tayfa_fifty", Year: 2027}},
		},
	} {
		if got := badge.Due(bs, c.totals); !slices.Equal(got, c.want) {
			t.Errorf("%s: %+v, beklenen %+v", name, got, c.want)
		}
	}
}

// A badge is earned once per period, notifies once, and is never taken
// back: a retired definition still names it (ADR-0039 §2).
func TestGrantUser(t *testing.T) {
	e := setup(t)
	u := id.New()
	grant := func(awards ...badge.Award) int {
		var n int
		e.tx(t, func(q *store.Queries) error {
			var err error
			n, err = badge.GrantUser(ctx, q, u, awards, e.now)
			return err
		})
		return n
	}
	if n := grant(badge.Award{Key: "first_event"}, badge.Award{Key: "yearly_hours", Year: 2026}); n != 2 {
		t.Fatalf("yeni %d", n)
	}
	if n := grant(badge.Award{Key: "first_event"}, badge.Award{Key: "yearly_hours", Year: 2026}); n != 0 {
		t.Fatalf("tekrar %d", n)
	}
	if n := grant(badge.Award{Key: "yearly_hours", Year: 2027}); n != 1 {
		t.Fatalf("yeni yıl %d", n)
	}
	ns := e.notices(t)
	if len(ns) != 2 {
		t.Fatalf("bildirimler = %+v", ns)
	}
	for _, n := range ns {
		if n.TayfaID != "" || n.TargetType != notify.TargetUser || n.TargetID != u.String() || !slices.Equal(n.Recipients, []string{u.String()}) {
			t.Fatalf("bildirim = %+v", n)
		}
	}
	var actors int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE type = $1 AND actor_id IS NOT NULL`, notify.EventType).Scan(&actors))
	if actors != 0 {
		t.Fatalf("aktörlü bildirim %d", actors)
	}

	_, err := e.admin.Exec(ctx, `UPDATE catalog_badges SET status = 'retired' WHERE key = 'first_event'`)
	must(t, err)
	got, err := badge.ForUser(ctx, store.New(e.pool), u)
	must(t, err)
	if !slices.Equal(keys(got), []string{"first_event", "yearly_hours", "yearly_hours"}) || got[0].Year != 0 || got[1].Year != 2026 || got[2].Year != 2027 ||
		!got[0].EarnedAt.Equal(e.now) || got[0].Event != (id.ID{}) || got[0].Tayfa != (id.ID{}) {
		t.Fatalf("rozetler = %+v", got)
	}
	// Someone else has none.
	if other, err := badge.ForUser(ctx, store.New(e.pool), id.New()); err != nil || other == nil || len(other) != 0 {
		t.Fatalf("başkası = %+v %v", other, err)
	}
}

// A tayfa badge goes to the tayfa and notifies its admins and moderators.
func TestGrantTayfa(t *testing.T) {
	e := setup(t)
	admin := id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Rozet Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	var n int
	e.tx(t, func(q *store.Queries) error {
		n, err = badge.GrantTayfa(ctx, q, v.ID, []badge.Award{{Key: "tayfa_fifty", Year: 2026}}, e.now)
		return err
	})
	if n != 1 {
		t.Fatalf("yeni %d", n)
	}
	e.tx(t, func(q *store.Queries) error {
		n, err = badge.GrantTayfa(ctx, q, v.ID, []badge.Award{{Key: "tayfa_fifty", Year: 2026}}, e.now)
		return err
	})
	if n != 0 {
		t.Fatalf("tekrar %d", n)
	}
	ns := e.notices(t)
	if len(ns) != 1 || ns[0].TayfaID != v.ID.String() || ns[0].TargetType != notify.TargetTayfa || ns[0].TargetID != v.ID.String() ||
		!slices.Equal(ns[0].Recipients, []string{admin.String()}) {
		t.Fatalf("bildirimler = %+v", ns)
	}
	got, err := badge.ForTayfa(ctx, store.New(e.pool), v.ID)
	must(t, err)
	if len(got) != 1 || got[0].Key != "tayfa_fifty" || got[0].Year != 2026 {
		t.Fatalf("tayfa rozetleri = %+v", got)
	}
	// The tayfa's GET carries them.
	view, _, err := e.tayfas.Get(ctx, admin, v.ID)
	must(t, err)
	if len(view.Badges) != 1 || view.Badges[0].Key != "tayfa_fifty" {
		t.Fatalf("tayfa görünümü = %+v", view.Badges)
	}
	// A personal badge cannot be a tayfa's.
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := badge.GrantTayfa(ctx, store.New(tx), v.ID, []badge.Award{{Key: "ten_events"}}, e.now)
		return err
	})
	if err == nil {
		t.Error("kişisel rozet tayfaya verildi")
	}
}

// event writes an event that ended long ago and, when winners are given,
// its announced celebration with those winners.
func (e *env) event(t *testing.T, typ string, winners ...id.ID) badge.Event {
	t.Helper()
	ev := badge.Event{ID: id.New(), Tayfa: id.New(), Type: typ, Version: 1}
	end := e.now.Add(-10 * 24 * time.Hour)
	e.seed(t, `INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, $3, 1, 'online', 'Final', $4, $5, 'https://example.test/final')`,
		ev.ID.PG(), ev.Tayfa.PG(), typ, end.Add(-2*time.Hour), end)
	if len(winners) == 0 {
		return ev
	}
	e.seed(t, `INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at) VALUES ($1, 'celebration', $2, $3, $3)`,
		ev.ID.PG(), id.New().PG(), end)
	for i, w := range winners {
		e.seed(t, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position) VALUES ($1, $2, 'celebration', $3, $4)`,
			id.New().PG(), ev.ID.PG(), w.PG(), i+1)
	}
	return ev
}

func (e *env) award(t *testing.T, ev badge.Event) int {
	t.Helper()
	bs := e.usable(t)
	var n int
	e.tx(t, func(q *store.Queries) error {
		var err error
		n, err = badge.AwardChampions(ctx, q, ev, bs, e.now)
		return err
	})
	return n
}

func (e *env) runs(t *testing.T, ev badge.Event) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM event_badge_runs WHERE event_id = $1`, ev.ID.PG()).Scan(&n))
	return n
}

// Champion badges (ADR-0039 §2): the announced winners get the active
// champion badges that fit the event's type, once; the badge stays when
// the event goes.
func TestAwardChampions(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	cup := e.event(t, cupType, a, b)
	if n := e.award(t, cup); n != 4 {
		t.Fatalf("yeni %d", n)
	}
	if e.runs(t, cup) != 1 {
		t.Fatal("etkinlik işaretlenmedi")
	}
	if n := e.award(t, cup); n != 0 {
		t.Fatalf("tekrar %d", n)
	}
	for _, u := range []id.ID{a, b} {
		got, err := badge.ForUser(ctx, store.New(e.pool), u)
		must(t, err)
		// The generic and the cup badge; not the race's, not the retired
		// one.
		if !slices.Equal(keys(got), []string{"champion", "cup_champion"}) && !slices.Equal(keys(got), []string{"cup_champion", "champion"}) {
			t.Fatalf("rozetler = %+v", got)
		}
		for _, g := range got {
			if g.Event != cup.ID || g.Tayfa != cup.Tayfa || g.Year != 0 {
				t.Fatalf("şampiyonluk = %+v", g)
			}
		}
	}
	if ns := e.notices(t); len(ns) != 2 {
		t.Fatalf("bildirimler = %+v", ns)
	}

	// A second final earns the generic badge again, for that event.
	again := e.event(t, cupType, a)
	if n := e.award(t, again); n != 2 {
		t.Fatalf("ikinci final %d", n)
	}
	// Never taken back: the event goes, the badges stay.
	e.seed(t, `DELETE FROM events WHERE id = $1`, cup.ID.PG())
	got, err := badge.ForUser(ctx, store.New(e.pool), a)
	must(t, err)
	if len(got) != 4 {
		t.Fatalf("silinen etkinliğin rozeti gitti: %+v", got)
	}
}

func TestAwardChampionsNothing(t *testing.T) {
	e := setup(t)
	winner := id.New()
	for name, ev := range map[string]badge.Event{
		// Not announced: no winner, nothing earned.
		"ilan yok": e.event(t, cupType),
		// Sensitive attendance never names a winner.
		"hassas katılım": e.event(t, bloodType, winner),
	} {
		if n := e.award(t, ev); n != 0 {
			t.Errorf("%s: %d rozet", name, n)
		}
		if e.runs(t, ev) != 1 {
			t.Errorf("%s: işaretlenmedi", name)
		}
	}
	got, err := badge.ForUser(ctx, store.New(e.pool), winner)
	must(t, err)
	if len(got) != 0 || len(e.notices(t)) != 0 {
		t.Fatalf("rozet verildi: %+v", got)
	}
}
