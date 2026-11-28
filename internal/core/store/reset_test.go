package store

import (
	"context"
	"strconv"
	"testing"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

const resetScript = "../../../db/core/reset/pilot-oncesi-sifirlama.sql"

// resetFixtures: a real tayfa with 3 members and a demo tayfa with 9, the
// audit log, profile copies, an upload, a catalog event and another event, a
// consumer position and a blocked domain added by an administrator.
const resetFixtures = `
SET session_replication_role = replica;
INSERT INTO tayfas (id, name, name_normalized, join_code, created_by)
VALUES ('00000000-0000-7000-8000-000000000001', 'Gerçek Tayfa', 'gerçek tayfa', 'ABCD2345', '00000000-0000-7000-8000-0000000000a1'),
       ('00000000-0000-7000-8000-000000000002', 'Ege Klasikleri Demo', 'ege klasikleri demo', 'EFGH2345', '00000000-0000-7000-8000-0000000000d1');
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
SELECT '00000000-0000-7000-8000-000000000001', ('00000000-0000-7000-8000-0000000000a' || g)::uuid, CASE WHEN g = 1 THEN 'admin' ELSE 'member' END, 'active', 'code', now()
FROM generate_series(1, 3) g;
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
SELECT '00000000-0000-7000-8000-000000000002', ('00000000-0000-7000-8000-0000000000d' || g)::uuid, 'member', 'active', 'code', now()
FROM generate_series(1, 9) g;
INSERT INTO audit_logs (id, tayfa_id, actor_id, action)
VALUES (gen_random_uuid(), '00000000-0000-7000-8000-000000000001', '00000000-0000-7000-8000-0000000000a1', 'tayfa.create');
INSERT INTO user_profiles (user_id, display_name, source_event_id)
SELECT ('00000000-0000-7000-8000-0000000000a' || g)::uuid, 'Test ' || g, gen_random_uuid() FROM generate_series(1, 3) g;
INSERT INTO domain_events (id, type, payload) VALUES
  ('00000000-0000-7000-8000-0000000000e1', 'catalog.changed', '{"catalog_version": 2, "interests": []}'),
  ('00000000-0000-7000-8000-0000000000e2', 'tayfa.created', '{}');
INSERT INTO event_consumers (consumer_name, last_event_id) VALUES ('identity.catalog', '00000000-0000-7000-8000-0000000000e1');
INSERT INTO blocked_domains (domain, created_by) VALUES ('ornek-engel.example', '00000000-0000-7000-8000-0000000000a1');
INSERT INTO uploads (id, user_id, purpose, status, r2_key, preview_type, preview_bytes, full_type, full_bytes, client_request_id)
VALUES (gen_random_uuid(), '00000000-0000-7000-8000-0000000000a1', 'avatar', 'pending', repeat('a', 32), 'image/jpeg', 10, 'image/jpeg', 20, gen_random_uuid());
SET session_replication_role = origin;
`

// resetMediaDone stands for core's media cleanup (media.Cleanup), which
// deletes the R2 objects and rows of the uploads the "medya" step queued.
const resetMediaDone = `DELETE FROM uploads`

// resetMembers adds members a<from> … a<to> (to ≤ 9) to the real tayfa.
func resetMembers(from, to int) string {
	return `SET session_replication_role = replica;
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
SELECT '00000000-0000-7000-8000-000000000001', ('00000000-0000-7000-8000-0000000000a' || g)::uuid, 'member', 'active', 'code', now()
FROM generate_series(` + strconv.Itoa(from) + `, ` + strconv.Itoa(to) + `) g;`
}

// TestPilotReset pins db/core/reset/pilot-oncesi-sifirlama.sql (issue
// #369, ADR-0046): tayfas, content, social data, reports, uploads, the
// audit log and consumer positions are emptied; the catalog, blocked
// domains, job runs and the migration history stay, and of the outbox only
// catalog.changed events. The threshold allows 6 distinct members of tayfas
// without "Demo" in the name and refuses the seventh; demo tayfa members
// are not counted.
func TestPilotReset(t *testing.T) {
	dbtest.CheckReset(t, dbtest.ResetSpec{
		Service:      "core",
		Migrations:   coredb.Migrations(),
		Script:       resetScript,
		OtherScript:  "../../../db/chat/reset/pilot-oncesi-sifirlama.sql",
		Fixtures:     resetFixtures,
		BeforeDelete: resetMediaDone,
		Kept: []string{
			"catalog_badges", "catalog_event_type_versions", "catalog_event_types", "catalog_interests",
			"catalog_meta", "catalog_score_presets", "catalog_study_tags",
			"blocked_domains", "job_runs", "goose_db_version",
		},
		Partial:     map[string]int64{"domain_events": 1},
		Filled:      []string{"tayfas", "memberships", "audit_logs", "user_profiles", "event_consumers"},
		AtThreshold: resetMembers(4, 6),
		OneMore:     resetMembers(7, 7),
	})
}

// TestPilotResetMedia: deleting is refused while uploads remain (their R2
// objects would be left behind); the "medya" step needs the same
// confirmation and queues every upload for core's media cleanup without
// deleting anything else; "tayfam.r2=atla" allows deleting in an
// environment without R2. The blocked domain stays without its creator and
// only the catalog event stays in the outbox.
func TestPilotResetMedia(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t, "core", coredb.Migrations())
	conn, err := d.Admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := conn.Hijack() // session_replication_role stays on this connection
	if _, err := fixtures.Exec(ctx, resetFixtures); err != nil {
		t.Fatal(err)
	}
	_ = fixtures.Close(ctx)
	confirm := func(mode string) map[string]string {
		return map[string]string{"tayfam.mod": mode, "tayfam.ortam": "prod", "tayfam.onay": "PROD-SIFIRLA"}
	}

	if _, err := dbtest.RunScript(t, d, resetScript, confirm("sil")); dbtest.SQLState(err) != "TS007" {
		t.Fatalf("uploads doluyken sil: %v, beklenen TS007", err)
	}
	if _, err := dbtest.RunScript(t, d, resetScript, map[string]string{"tayfam.mod": "medya"}); dbtest.SQLState(err) != "TS002" {
		t.Fatalf("onaysız medya: %v, beklenen TS002", err)
	}
	before := dbtest.TableCounts(t, d.Admin)
	if _, err := dbtest.RunScript(t, d, resetScript, confirm("medya")); err != nil {
		t.Fatalf("medya: %v", err)
	}
	var waiting, total int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'deleting'), count(*) FROM uploads`).Scan(&waiting, &total); err != nil {
		t.Fatal(err)
	}
	if total == 0 || waiting != total {
		t.Fatalf("medya adımı: %d/%d yükleme silme kuyruğunda", waiting, total)
	}
	for table, n := range dbtest.TableCounts(t, d.Admin) {
		if n != before[table] {
			t.Errorf("medya adımı %s tablosunu değiştirdi: %d → %d", table, before[table], n)
		}
	}

	withoutR2 := confirm("sil")
	withoutR2["tayfam.r2"] = "atla"
	if _, err := dbtest.RunScript(t, d, resetScript, withoutR2); err != nil {
		t.Fatalf("r2=atla ile sil: %v", err)
	}
	var others, catalog int
	if err := d.Admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE type <> 'catalog.changed'), count(*) FILTER (WHERE type = 'catalog.changed')
		FROM domain_events`).Scan(&others, &catalog); err != nil {
		t.Fatal(err)
	}
	if others != 0 || catalog != 1 {
		t.Errorf("outbox: %d başka olay, %d katalog olayı kaldı; beklenen 0 ve 1", others, catalog)
	}
	var withCreator, domains int
	if err := d.Admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE created_by IS NOT NULL), count(*) FROM blocked_domains`).Scan(&withCreator, &domains); err != nil {
		t.Fatal(err)
	}
	if withCreator != 0 || domains != int(before["blocked_domains"]) {
		t.Errorf("blocked_domains: ekleyeni dolu %d, toplam %d (önce %d)", withCreator, domains, before["blocked_domains"])
	}
}
