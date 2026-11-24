package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// 00070 adds the scoreboard's pair side (issue #343; ADR-0044): pair
// tables of at most four seats, four pair presets and the board game
// night's second version offering them. Up again changes only the catalog
// version; Down stops at an event made with version 2 and otherwise
// deletes the pair tables and brings the old catalog back.
func TestScoreboardPairsMigration(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00070_scoreboard_pairs.sql")
	exec := func(sql string, args ...any) error {
		_, err := admin.Exec(ctx, sql, args...)
		return err
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// state is the board game night's version, its version rows and the
	// presets in catalog order.
	state := func() string {
		t.Helper()
		return scalar(`SELECT concat_ws(' | ',
			(SELECT version::text FROM catalog_event_types WHERE key = 'board_game_night'),
			(SELECT string_agg(version::text, ',' ORDER BY version) FROM catalog_event_type_versions WHERE type_key = 'board_game_night'),
			(SELECT string_agg(key || ':' || position, ',' ORDER BY position) FROM catalog_score_presets))`)
	}
	version := func() string {
		t.Helper()
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}
	constraint := func(err error) string {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return pg.ConstraintName
		}
		return ""
	}

	const migrated = "2 | 1,2 | batak:10,pair_batak:11,okey_101:20,pair_101:21,okey:30,pair_tiles:31," +
		"backgammon:40,pisti:50,pair_pisti:51,foosball:60,team_rounds:70,free:80"
	if got := state(); got != migrated {
		t.Fatalf("migration sonrası = %s", got)
	}
	// Version 2 changes only the scoreboard's presets.
	if got := scalar(`SELECT count(*)::text FROM catalog_event_type_versions AS v1
		JOIN catalog_event_type_versions AS v2 ON v2.type_key = v1.type_key AND v2.version = 2
		WHERE v1.type_key = 'board_game_night' AND v1.version = 1
		  AND v1.definition -> 'components' -> 1 = v2.definition -> 'components' -> 1
		  AND v1.definition -> 'components' -> 2 = v2.definition -> 'components' -> 2
		  AND jsonb_array_length(v2.definition -> 'components' -> 0 -> 'params' -> 'presets') = 11
		  AND v1.labels = v2.labels AND v1.modes = v2.modes AND v1.icon = v2.icon`); got != "1" {
		t.Fatalf("yalnızca ön ayarları değişen sürüm = %s", got)
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

	// A pair table (on a version 1 event, which Down does not stop at):
	// four seats, never a fifth.
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	ev1, err := insertEvent(ctx, pool, tayfa, "board_game_night", "in_person", map[string]string{"type_version": "1"})
	if err != nil {
		t.Fatal(err)
	}
	table := id.New()
	if err := exec(`INSERT INTO event_score_tables (id, event_id, component_key, label, preset_key, side, rounds, winner, result)
		VALUES ($1, $2, 'scoreboard', 'Eşli', 'pair_101', 'pair', 'round', 'low', 'points')`, table.PG(), ev1.PG()); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result, team_key)
		VALUES ($1, $2, 'scoreboard', 'Takımlı eşli', 'pair', 'round', 'low', 'points', 'teams')`, id.New().PG(), ev1.PG()); constraint(err) != "event_score_tables_side" {
		t.Fatalf("takım anahtarlı eşli masa: %v", err)
	}
	seat := func(position int) error {
		return exec(`INSERT INTO event_score_players (id, table_id, event_id, component_key, position, user_id)
			VALUES ($1, $2, $3, 'scoreboard', $4, $5)`, id.New().PG(), table.PG(), ev1.PG(), position, id.New().PG())
	}
	for pos := 1; pos <= 4; pos++ {
		if err := seat(pos); err != nil {
			t.Fatal(err)
		}
	}
	if err := seat(5); constraint(err) != "event_score_players_pair" {
		t.Fatalf("beşinci koltuk: %v", err)
	}
	if err := exec(`INSERT INTO event_scores (id, table_id, event_id, component_key, player_id, round, points)
		SELECT gen_random_uuid(), table_id, event_id, component_key, id, 1, 10 FROM event_score_players WHERE table_id = $1`, table.PG()); err != nil {
		t.Fatal(err)
	}

	// Down stops at an event made with version 2, and changes nothing.
	ev2, err := insertEvent(ctx, pool, tayfa, "board_game_night", "in_person", map[string]string{"type_version": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := exec(down); err == nil {
		t.Fatal("sürüm 2 etkinliği varken geri alındı")
	}
	if got := state(); got != migrated {
		t.Fatalf("başarısız geri almadan sonra = %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_scores WHERE table_id = $1`, table.PG()); got != "4" {
		t.Fatalf("başarısız geri almadan sonra skorlar = %s", got)
	}
	if err := exec(`DELETE FROM events WHERE id = $1`, ev2.PG()); err != nil {
		t.Fatal(err)
	}

	// Down: the pair table goes with its seats and scores, the old
	// catalog is back, the trigger that keeps versions forward is on.
	before = version()
	if err := exec(down); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "1 | 1 | batak:1,okey_101:2,okey:3,backgammon:4,pisti:5,foosball:6,team_rounds:7,free:8" {
		t.Fatalf("geri alınan = %s", got)
	}
	if got := scalar(`SELECT concat_ws('/',
		(SELECT count(*) FROM event_score_tables WHERE id = $1),
		(SELECT count(*) FROM event_score_players WHERE table_id = $1),
		(SELECT count(*) FROM event_scores WHERE table_id = $1))`, table.PG()); got != "0/0/0" {
		t.Fatalf("geri almada eşli masa = %s", got)
	}
	if version() == before {
		t.Fatal("geri almada katalog sürümü artmadı")
	}
	if got := scalar(`SELECT tgenabled::text FROM pg_trigger WHERE tgname = 'catalog_event_types_version_forward'`); got != "O" {
		t.Fatalf("sürüm tetikleyicisi = %s", got)
	}
	if err := exec(`INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result)
		VALUES ($1, $2, 'scoreboard', 'Eşli', 'pair', 'round', 'low', 'points')`, id.New().PG(), ev1.PG()); constraint(err) != "event_score_tables_side" {
		t.Fatalf("geri almadan sonra eşli masa: %v", err)
	}

	// Up again brings the same state.
	if err := exec(up); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != migrated {
		t.Fatalf("yeniden kurulan = %s", got)
	}
}
