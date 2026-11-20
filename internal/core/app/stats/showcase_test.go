package stats_test

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	walkType = "test_stat_walk"
	walkDef  = `{"components":[
 {"kind":"measure","key":"distance","labels":{"tr":"Mesafe"},"params":{"unit":"km","scope":"total","entered_by":"organizer","decimals":1,"showcase":true}},
 {"kind":"measure","key":"bags","labels":{"tr":"Torba"},"params":{"unit":"piece","scope":"total","entered_by":"organizer"}}]}`
)

// TestShowcase (issue #267, ADR-0026 §3): no card below three frozen
// events; with three, the all-time totals the showcase may carry and the
// tayfa's badges. A measure without showcase, a log metric and anything
// of a type whose attendance is sensitive stay out; the sensitive event
// still counts in the overall events and attended.
func TestShowcase(t *testing.T) {
	e := setup(t)
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9001, '{"tr": "Yürüyüş"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, walkType, walkDef)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, walkType)
	_, err := e.admin.Exec(ctx, `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('test_show_badge', 'tayfa', 1, '{"tr": "Rozet"}', 'crown', '{"kind": "threshold", "metric": "events", "value": 100, "period": "all_time"}'::jsonb)`)
	must(t, err)

	tf, _ := e.newTayfa(t)
	a, b := e.member(t, tf), e.member(t, tf)
	now := time.Now().UTC().Truncate(time.Microsecond)
	day := time.Date(2025, 5, 1, 7, 0, 0, 0, time.UTC)
	w1 := e.event(t, tf, walkType, day, a, b)
	e.measure(t, w1, "distance", id.ID{}, 12.5)
	e.measure(t, w1, "bags", id.ID{}, 4)
	f1 := e.event(t, tf, fishType, day.AddDate(0, 0, 7), a)
	e.measure(t, f1, "distance", id.ID{}, 3)
	e.catch(t, f1, a, e.word(t, tf, "Levrek"), 1)
	e.recompute(t, tf, now)

	r := stats.NewShowcaseReader(store.New(e.pool))
	if sc, ok, err := r.Tayfa(ctx, tf); err != nil || ok || sc.Stats != nil {
		t.Fatalf("iki etkinlikte vitrin: %v %v %v", sc, ok, err)
	}

	bl := e.event(t, tf, bloodType, day.AddDate(0, 0, 14), a)
	e.measure(t, bl, "units", id.ID{}, 1)
	e.recompute(t, tf, now)
	e.exec(t, `INSERT INTO tayfa_badges (id, tayfa_id, badge_key, period_key, earned_at) VALUES ($1, $2, 'test_show_badge', 'all', $3)`,
		id.New().PG(), tf.PG(), now)

	sc, ok, err := stats.NewShowcaseReader(store.New(e.pool)).Tayfa(ctx, tf)
	must(t, err)
	if !ok {
		t.Fatal("üç etkinlikte vitrin yok")
	}
	got := map[string]float64{}
	for _, s := range sc.Stats {
		got[s.Metric] = s.Value
	}
	want := map[string]float64{
		"events": 3, "attended": 4, "participants": 2, "duration": 120,
		walkType + ".events": 1, walkType + ".attended": 2, walkType + ".participants": 2, walkType + ".duration": 60,
		walkType + ".distance": 12.5,
		fishType + ".events":   1, fishType + ".attended": 1, fishType + ".participants": 1, fishType + ".duration": 60,
	}
	if len(got) != len(want) {
		t.Errorf("vitrin metrikleri: %v, beklenen %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, %v bekleniyordu", k, got[k], v)
		}
	}
	for k := range got {
		for _, banned := range []string{walkType + ".bags", fishType + ".distance", fishType + ".catches.count", bloodType + ".events", bloodType + ".units"} {
			if k == banned {
				t.Errorf("vitrinde olmaması gereken metrik: %s", k)
			}
		}
	}
	if len(sc.Badges) != 1 || sc.Badges[0].Key != "test_show_badge" {
		t.Fatalf("vitrin rozetleri: %+v", sc.Badges)
	}
}
