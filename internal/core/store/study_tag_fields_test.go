package store

import (
	"context"
	"testing"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// 00068 gives the four Eğitim types a second version with a study tag
// field (issue #333; ADR-0043 §6). Up again changes only the catalog
// version; Down brings version 1 back (past the forward-only trigger,
// which it leaves on) unless an event was made with version 2.
func TestStudyTagFieldsMigration(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00068_study_tag_fields.sql")
	exec := func(sql string) error {
		_, err := admin.Exec(ctx, sql)
		return err
	}
	scalar := func(sql string) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, sql).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	const studyTypes = `('study_group', 'problem_session', 'peer_teaching', 'mock_exam')`
	// state is the four types' versions and field kinds, and their
	// version rows.
	state := func() string {
		t.Helper()
		return scalar(`SELECT concat_ws(' | ',
			(SELECT string_agg(key || ':' || version || ':' || COALESCE(definition -> 'fields' -> 0 ->> 'kind', '-'), ',' ORDER BY key)
			   FROM catalog_event_types WHERE key IN ` + studyTypes + `),
			(SELECT string_agg(type_key || ':' || version, ',' ORDER BY type_key, version)
			   FROM catalog_event_type_versions WHERE type_key IN ` + studyTypes + `))`)
	}
	version := func() string {
		t.Helper()
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}
	triggerOn := func() bool {
		t.Helper()
		return scalar(`SELECT tgenabled::text FROM pg_trigger WHERE tgname = 'catalog_event_types_version_forward'`) == "O"
	}

	const migrated = "mock_exam:2:study_tags,peer_teaching:2:study_tags,problem_session:2:study_tags,study_group:2:study_tags | " +
		"mock_exam:1,mock_exam:2,peer_teaching:1,peer_teaching:2,problem_session:1,problem_session:2,study_group:1,study_group:2"
	if got := state(); got != migrated {
		t.Fatalf("migration sonrası = %s", got)
	}
	// The current definition is the version 2 row.
	if got := scalar(`SELECT count(*)::text FROM catalog_event_types AS t
		JOIN catalog_event_type_versions AS v ON v.type_key = t.key AND v.version = t.version
		WHERE t.key IN ` + studyTypes + ` AND t.definition = v.definition AND t.labels = v.labels AND t.modes = v.modes AND t.icon = v.icon`); got != "4" {
		t.Fatalf("sürüm 2 ile aynı türler = %s", got)
	}
	// Version 2 adds the field only: the components are version 1's.
	if got := scalar(`SELECT count(*)::text FROM catalog_event_type_versions AS v1
		JOIN catalog_event_type_versions AS v2 ON v2.type_key = v1.type_key AND v2.version = 2
		WHERE v1.version = 1 AND v1.type_key IN ` + studyTypes + `
		  AND v1.definition -> 'components' = v2.definition -> 'components' AND NOT v1.definition ? 'fields'
		  AND v1.labels = v2.labels AND v1.modes = v2.modes AND v1.icon = v2.icon`); got != "4" {
		t.Fatalf("yalnızca alan eklenen türler = %s", got)
	}

	// Up is idempotent but for the catalog version.
	before := version()
	if err := exec(up); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != migrated {
		t.Fatalf("yeniden çalışınca = %s", got)
	}
	if version() == before {
		t.Fatal("katalog sürümü artmadı")
	}

	// Down stops at an event made with version 2, and changes nothing.
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	eid, err := insertEvent(ctx, pool, tayfa, "peer_teaching", "in_person", map[string]string{"type_version": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := scalar(`SELECT type_version::text FROM events WHERE id = '` + eid.String() + `'`); got != "2" {
		t.Fatalf("etkinliğin sürümü = %s", got)
	}
	if err := exec(down); err == nil {
		t.Fatal("sürüm 2 etkinliği varken geri alındı")
	}
	if got := state(); got != migrated || !triggerOn() {
		t.Fatalf("başarısız geri almadan sonra = %s, tetikleyici açık = %v", got, triggerOn())
	}
	if err := exec(`DELETE FROM events WHERE id = '` + eid.String() + `'`); err != nil {
		t.Fatal(err)
	}

	// Down: version 1 again, the trigger back on.
	before = version()
	if err := exec(down); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "mock_exam:1:-,peer_teaching:1:-,problem_session:1:-,study_group:1:-"+
		" | mock_exam:1,peer_teaching:1,problem_session:1,study_group:1" {
		t.Fatalf("geri alınan = %s", got)
	}
	if !triggerOn() || version() == before {
		t.Fatalf("tetikleyici açık = %v, katalog sürümü %s → %s", triggerOn(), before, version())
	}
	if got := scalar(`SELECT count(*)::text FROM catalog_event_types AS t
		JOIN catalog_event_type_versions AS v ON v.type_key = t.key AND v.version = t.version
		WHERE t.key IN ` + studyTypes + ` AND t.definition = v.definition`); got != "4" {
		t.Fatalf("sürüm 1 ile aynı türler = %s", got)
	}
	// The trigger still keeps the version from going back.
	if err := exec(`UPDATE catalog_event_types SET version = 1 WHERE key = 'convoy'`); err == nil {
		t.Fatal("tür sürümü geri gitti")
	}

	// Up again brings the same second version.
	if err := exec(up); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != migrated {
		t.Fatalf("yeniden kurulan = %s", got)
	}
}
