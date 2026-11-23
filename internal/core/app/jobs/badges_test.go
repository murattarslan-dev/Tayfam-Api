package jobs

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// seedAdmin runs a fixture statement as a superuser with triggers off.
func (e *env) seedAdmin(t *testing.T, sql string, args ...any) {
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

// celebrated writes an event of typ that ended at base + ended, with an
// announced celebration naming winner.
func (e *env) celebrated(t *testing.T, typ string, ended time.Duration, winner id.ID) id.ID {
	t.Helper()
	ev := id.New()
	end := e.base.Add(ended)
	e.seedAdmin(t, `INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, $3, 1, 'online', 'Final', $4, $5, 'https://example.test/final')`, ev.PG(), id.New().PG(), typ, end.Add(-time.Hour), end)
	e.seedAdmin(t, `INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at) VALUES ($1, 'celebration', $2, $3, $3)`,
		ev.PG(), id.New().PG(), end)
	e.seedAdmin(t, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position) VALUES ($1, $2, 'celebration', $3, 1)`,
		id.New().PG(), ev.PG(), winner.PG())
	return ev
}

// TestAwardChampionBadges (issue #258; ADR-0039 §2): an event is looked at
// once, after its celebration froze; an event that has no celebration, or
// is still in its correction window, is not.
func TestAwardChampionBadges(t *testing.T) {
	e := setup(t)
	for key, def := range map[string]string{"test_job_cup": `{"components":[{"kind":"celebration"}]}`, "test_job_plain": `{"components":[{"kind":"route"}]}`} {
		e.seedAdmin(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
			VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
		e.seedAdmin(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
			SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
	}
	// Only this badge: the seed's list (issue #260) is replaced.
	_, err := e.admin.Exec(ctx, `DELETE FROM catalog_badges;
		INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('champion', 'user', 1, '{"tr": "Şampiyon"}', 'crown', '{"kind": "champion"}')`)
	must(t, err)
	frozenWinner, openWinner, plainWinner := id.New(), id.New(), id.New()
	e.celebrated(t, "test_job_cup", -component.FreezeAfter, frozenWinner)
	e.celebrated(t, "test_job_cup", -component.FreezeAfter+time.Minute, openWinner)
	e.celebrated(t, "test_job_plain", -30*24*time.Hour, plainWinner)
	badges := func(u id.ID) int {
		return e.count(t, `SELECT count(*) FROM user_badges WHERE user_id = $1`, u.PG())
	}

	run(t, "donmuş", e.at(t, 0).AwardChampionBadges, 1)
	if badges(frozenWinner) != 1 || badges(openWinner) != 0 || badges(plainWinner) != 0 {
		t.Fatalf("rozetler: donmuş %d, açık %d, kutlamasız %d", badges(frozenWinner), badges(openWinner), badges(plainWinner))
	}
	run(t, "tekrar", e.at(t, 0).AwardChampionBadges, 0)
	run(t, "dakika sonra", e.at(t, time.Minute).AwardChampionBadges, 1)
	if badges(openWinner) != 1 {
		t.Fatal("pencere kapanınca rozet verilmedi")
	}
}
