package store

import (
	"context"
	"testing"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
)

// 00066 is the catalog's seed (issue #260): running its Up again changes
// no row but the catalog version (the seed is idempotent); its Down leaves
// the catalog as 00065 left it (the seven Faz 1 types at version 1, six
// interests, no study tags or badges), and Up brings the same seed back.
func TestCatalogDataMigration(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00066_catalog_data.sql")
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, sql).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// snapshot is every catalog row 00066 writes, as one text.
	snapshot := func() string {
		t.Helper()
		return scalar(`SELECT concat_ws(' | ',
			(SELECT string_agg(concat_ws(',', key, position, labels::text, icon, status, default_widgets::text), ';' ORDER BY key) FROM catalog_interests),
			(SELECT string_agg(concat_ws(',', key, interest_keys::text, position, labels::text, icon, modes::text, status, version, definition::text), ';' ORDER BY key) FROM catalog_event_types),
			(SELECT string_agg(concat_ws(',', type_key, version, labels::text, icon, modes::text, definition::text), ';' ORDER BY type_key, version) FROM catalog_event_type_versions),
			(SELECT string_agg(concat_ws(',', key, parent_key, position, labels::text, status), ';' ORDER BY key) FROM catalog_study_tags),
			(SELECT string_agg(concat_ws(',', key, scope, position, labels::text, icon, rule::text, status), ';' ORDER BY key) FROM catalog_badges),
			(SELECT string_agg(concat_ws(',', key, position, labels::text, params::text, status), ';' ORDER BY key) FROM catalog_score_presets))`)
	}
	counts := func() string {
		t.Helper()
		return scalar(`SELECT concat_ws('/',
			(SELECT count(*) FROM catalog_interests), (SELECT count(*) FROM catalog_event_types),
			(SELECT count(*) FROM catalog_event_type_versions), (SELECT count(*) FROM catalog_study_tags),
			(SELECT count(*) FROM catalog_badges),
			(SELECT count(*) FROM catalog_event_types WHERE version = 2),
			(SELECT count(*) FROM catalog_interests WHERE default_widgets = ARRAY['upcoming', 'announcements', 'polls', 'questions']))`)
	}
	version := func() string {
		t.Helper()
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}

	// 15 interests, 65 types (58 new; six Faz 1 types and, since 00068,
	// the four Eğitim types at version 2), the study tag list and five
	// badges.
	seeded, before := snapshot(), version()
	if got := counts(); got != "15/65/75/294/5/10/15" {
		t.Fatalf("seed = %s", got)
	}

	exec(up)
	if snapshot() != seeded {
		t.Fatal("seed yeniden çalışınca katalog değişti")
	}
	if got := version(); got == before {
		t.Fatal("katalog sürümü artmadı")
	}

	// 00068's and 00067's Downs first, as on the real way back: 00068 gave
	// the Eğitim types their second version (#333), 00067's trigger keeps a
	// type's version and the catalog's version from going back (#262).
	studyUp, studyDown := migrationParts(t, coredb.Migrations(), "00068_study_tag_fields.sql")
	_, panelDown := migrationParts(t, coredb.Migrations(), "00067_catalog_panel.sql")
	exec(studyDown)
	exec(panelDown)
	exec(down)
	if got := counts(); got != "6/7/7/0/0/0/0" {
		t.Fatalf("geri alınan katalog = %s", got)
	}
	if got := scalar(`SELECT string_agg(key || ':' || position || ':' || version, ',' ORDER BY position) FROM catalog_event_types`); got !=
		"convoy:10:1,hike:20:1,camp:30:1,fishing:40:1,sports:50:1,gaming:60:1,general:900:1" {
		t.Fatalf("geri alınan türler = %s", got)
	}
	// The current definition is the version 1 row again.
	if got := scalar(`SELECT count(*)::text FROM catalog_event_types AS t
		JOIN catalog_event_type_versions AS v ON v.type_key = t.key AND v.version = t.version
		WHERE t.definition = v.definition AND t.labels = v.labels AND t.modes = v.modes AND t.icon = v.icon`); got != "7" {
		t.Fatalf("sürüm 1 ile aynı türler = %s", got)
	}

	exec(up)
	exec(studyUp)
	if snapshot() != seeded {
		t.Fatal("geri alıp yeniden kurunca katalog farklı")
	}
}
