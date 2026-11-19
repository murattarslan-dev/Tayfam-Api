package store

import (
	"context"
	"maps"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// core_app's table privileges (db/core/migrations/00016_app_role.sql,
// ADR-0015). The append-only tables get only the narrow exceptions the
// account deletion consumer and the retention job need, each limited by its
// trigger. A new table or grant must be added here on purpose.
var wantAppPrivileges = map[string]string{
	"tayfas":                "DELETE,INSERT,SELECT,UPDATE",
	"memberships":           "DELETE,INSERT,SELECT,UPDATE",
	"announcements":         "DELETE,INSERT,SELECT,UPDATE",
	"questions":             "DELETE,INSERT,SELECT,UPDATE",
	"answers":               "DELETE,INSERT,SELECT,UPDATE",
	"polls":                 "DELETE,INSERT,SELECT,UPDATE",
	"events":                "DELETE,INSERT,SELECT,UPDATE",
	"event_rsvps":           "DELETE,INSERT,SELECT,UPDATE",
	"user_activity_records": "DELETE,INSERT,SELECT,UPDATE",
	// Issue #206: a photo never moves to another event, file or uploader.
	"event_photos": "DELETE,INSERT,SELECT,UPDATE(deleted_at,deleted_by)",
	"friendships":  "DELETE,INSERT,SELECT,UPDATE",
	// Issue #208, ADR-0030: scores are rewritten daily, never updated;
	// a suggestion only gets its dismissal and withdrawal times.
	"friend_scores":           "DELETE,INSERT,SELECT",
	"friend_suggestions":      "DELETE,INSERT,SELECT,UPDATE(dismissed_at,withdrawn_at)",
	"social_settings":         "DELETE,INSERT,SELECT,UPDATE(discover_visible_to_friends,hide_from_suggestions,profiling_enabled,updated_at)",
	"friend_request_outcomes": "DELETE,INSERT,SELECT,UPDATE(outcome,resolved_at)",
	// Issue #210, ADR-0033: an application is never deleted (it goes with
	// the tayfa); feedback carries no text; a safety contact row never
	// moves to another event or participant.
	"tayfa_discover":        "INSERT,SELECT,UPDATE(drop_reason,masked_status,masked_until,requested_at,requested_by,reviewed_at,reviewed_by,status,status_changed_at,updated_at)",
	"discover_joins":        "DELETE,INSERT,SELECT,UPDATE(joined_at,left_at)",
	"event_feedback":        "DELETE,INSERT,SELECT,UPDATE(answer,updated_at)",
	"event_safety_contacts": "DELETE,INSERT,SELECT,UPDATE(alert_sent_at,contact_user_id,grace_minutes,returned_at,updated_at)",
	"invitation_outcomes":   "DELETE,INSERT,SELECT,UPDATE(outcome,resolved_at)",
	"reports":               "DELETE,INSERT,SELECT,UPDATE",
	"tayfa_interests":       "DELETE,INSERT,SELECT",
	"answer_votes":          "DELETE,INSERT,SELECT",
	"question_tags":         "DELETE,INSERT,SELECT",
	"tags":                  "DELETE,INSERT,SELECT",
	"poll_votes":            "DELETE,INSERT,SELECT",
	"user_blocks":           "DELETE,INSERT,SELECT",
	"invites":               "INSERT,SELECT,UPDATE",
	"tayfa_modules":         "INSERT,SELECT,UPDATE",
	"user_profiles":         "INSERT,SELECT,UPDATE",
	"event_series":          "INSERT,SELECT,UPDATE",
	"poll_options":          "INSERT,SELECT",
	"event_consumers":       "INSERT,SELECT,UPDATE",
	"domain_events":         "INSERT,SELECT,UPDATE(actor_id,tayfa_id)",
	"audit_logs":            "DELETE,INSERT,SELECT,UPDATE(actor_id)",
	"user_platform_bans":    "DELETE,INSERT,SELECT,UPDATE(banned_at,event_id)",
	"job_runs":              "INSERT,SELECT,UPDATE(changed,finished_at,last_succeeded_at,succeeded)",
	// Issue #135: the key, purpose, declared type and size never change.
	"uploads": "DELETE,INSERT,SELECT,UPDATE(attach_confirmed_at,attached_at,attached_id,attached_type,expires_at,export_bytes,height,ready_at,reject_reason,status,updated_at,user_id,width)",
	// Issue #207: user_id and day never change.
	"user_availability": "DELETE,INSERT,SELECT,UPDATE(note,slots,updated_at)",
	// Issue #237, ADR-0025: the service only reads the catalog; it changes
	// through migrations (and later the platform panel, #262).
	"catalog_meta":                "SELECT",
	"catalog_interests":           "SELECT",
	"catalog_event_types":         "SELECT",
	"catalog_event_type_versions": "SELECT",
	// Issue #237: a value never moves to another event or field.
	"event_field_values": "DELETE,INSERT,SELECT,UPDATE(updated_at,value)",
	// Issue #238: a selection never moves to another event or component
	// and goes only with its event; a referee assignment is never changed,
	// only removed.
	"event_components": "INSERT,SELECT,UPDATE(enabled,params,updated_at)",
	"event_referees":   "DELETE,INSERT,SELECT",
	// Issue #239: a route never moves to another event or component.
	"event_routes": "DELETE,INSERT,SELECT,UPDATE(points,updated_at)",
	// Issue #240: a vehicle never moves to another event, component or
	// person; a seat request is only approved or removed.
	"event_vehicles":              "DELETE,INSERT,SELECT,UPDATE(color,make,model,model_year,plate,seats,updated_at)",
	"event_vehicle_seat_requests": "DELETE,INSERT,SELECT,UPDATE(decided_at,status)",
	// Issue #241: an item never moves to another event or list, and its
	// name and quantity do not change; only the assignee changes and
	// account deletion clears the people on it.
	"event_checklist_items": "DELETE,INSERT,SELECT,UPDATE(assigned_user_id,created_by,updated_at)",
	// Issue #242: a plan or a roll call record never moves to another
	// event, component or person; a record's start never changes, only its
	// return and alarm are written.
	"event_roll_calls":         "DELETE,INSERT,SELECT,UPDATE(expected_return_at,updated_at)",
	"event_roll_call_checkins": "DELETE,INSERT,SELECT,UPDATE(alert_sent_at,returned_at,returned_received_at,updated_at)",
	// Issue #243: a log entry never moves to another event, component or
	// person, and its values, location and photo do not change; it is only
	// soft deleted (account deletion clears who deleted it).
	"event_log_entries": "DELETE,INSERT,SELECT,UPDATE(deleted_at,deleted_by)",
	// Issue #244: a vocabulary value is never deleted and its spelling never
	// changes; only a merge and account deletion write to it. A log entry's
	// link never changes; it goes with the entry (cascade).
	"tayfa_vocabulary":           "INSERT,SELECT,UPDATE(created_by,merged_into)",
	"event_log_entry_vocabulary": "INSERT,SELECT",
}

func TestAppRolePrivileges(t *testing.T) {
	pool, _ := setup(t)
	if got := dbtest.TablePrivileges(t, pool); !maps.Equal(got, wantAppPrivileges) {
		t.Fatalf("core_app yetkileri:\n  %v\nbeklenen:\n  %v", got, wantAppPrivileges)
	}
}

// If the service is taken over, its database user cannot switch off the
// triggers that guard memberships and the audit log, erase the audit log or
// outbox, or change the schema (ADR-0011 hardening item 8).
func TestAppRoleCannotBypassGuards(t *testing.T) {
	pool, _ := setup(t)
	for name, sql := range map[string]string{
		"denetim trigger'ını kapatma":   `ALTER TABLE audit_logs DISABLE TRIGGER audit_logs_append_only`,
		"tüm trigger'ları kapatma":      `ALTER TABLE audit_logs DISABLE TRIGGER ALL`,
		"üyelik trigger'ını kapatma":    `ALTER TABLE memberships DISABLE TRIGGER memberships_guard`,
		"trigger'ı silme":               `DROP TRIGGER memberships_guard ON memberships`,
		"trigger fonksiyonunu değiştir": `CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		"replica modu":                  `SET session_replication_role = replica`,
		"denetim kaydını boşaltma":      `TRUNCATE audit_logs`,
		"üyelikleri boşaltma":           `TRUNCATE memberships`,
		"denetim kaydını güncelleme":    `UPDATE audit_logs SET action = 'x.y'`,
		"denetim tayfasını değiştirme":  `UPDATE audit_logs SET tayfa_id = tayfa_id`,
		"olayı güncelleme":              `UPDATE domain_events SET type = 'x.y'`,
		"olay payload'ı":                `UPDATE domain_events SET payload = '{}'`,
		"olayı silme":                   `DELETE FROM domain_events`,
		"olayları boşaltma":             `TRUNCATE domain_events`,
		"anket trigger'ı":               `ALTER TABLE polls DISABLE TRIGGER polls_guard`,
		"oy trigger'ı":                  `ALTER TABLE poll_votes DISABLE TRIGGER poll_votes_no_update`,
		"şikâyet trigger'ı":             `ALTER TABLE reports DISABLE TRIGGER reports_guard`,
		"platform kararı trigger'ı":     `ALTER TABLE reports DISABLE TRIGGER reports_platform_guard`,
		"ban kaydının kullanıcısı":      `UPDATE user_platform_bans SET user_id = user_id`,
		"banları boşaltma":              `TRUNCATE user_platform_bans`,
		"iş kaydını silme":              `DELETE FROM job_runs`,
		"iş adını değiştirme":           `UPDATE job_runs SET job = 'x'`,
		"sistem işi fonksiyonu":         `CREATE OR REPLACE FUNCTION current_system_job() RETURNS text LANGUAGE sql AS $$ SELECT 'platform_admin' $$`,
		"anket seçeneğini değiştirme":   `UPDATE poll_options SET position = position`,
		"relay konumunu silme":          `DELETE FROM event_consumers`,
		"tablo silme":                   `DROP TABLE audit_logs`,
		"kolon ekleme":                  `ALTER TABLE tayfas ADD COLUMN x int`,
		"kısıtı kaldırma":               `ALTER TABLE memberships DROP CONSTRAINT memberships_role`,
		"tablo açma":                    `CREATE TABLE x (id int)`,
		"geçici tablo":                  `CREATE TEMP TABLE x (id int)`,
		"trigger ekleme":                `CREATE TRIGGER x BEFORE INSERT ON tayfas FOR EACH ROW EXECUTE FUNCTION set_updated_at()`,
		"migration sürümünü okuma":      `SELECT * FROM goose_db_version`,
		"migration sürümü ekleme":       `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`,
		"sahipliği alma":                `ALTER TABLE audit_logs OWNER TO core_app`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}

// Tables added by later migrations get SELECT and INSERT only; UPDATE and
// DELETE are granted explicitly.
func TestAppRoleDefaultPrivileges(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tx, err := adminOf(t, pool).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE core`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE later_table (id int)`); err != nil {
		t.Fatal(err)
	}
	var privs string
	if err := tx.QueryRow(ctx, `
		SELECT string_agg(privilege_type, ',' ORDER BY privilege_type)
		FROM information_schema.table_privileges
		WHERE grantee = 'core_app' AND table_name = 'later_table'`).Scan(&privs); err != nil {
		t.Fatal(err)
	}
	if privs != "INSERT,SELECT" {
		t.Fatalf("yeni tablonun varsayılan yetkileri = %q", privs)
	}
}

func TestAppRoleDatabaseAccess(t *testing.T) {
	pool, _ := setup(t)
	var connect, temp, create bool
	if err := pool.QueryRow(context.Background(), `
		SELECT has_database_privilege(current_database(), 'CONNECT'),
		       has_database_privilege(current_database(), 'TEMP'),
		       has_schema_privilege('public', 'CREATE')`).Scan(&connect, &temp, &create); err != nil {
		t.Fatal(err)
	}
	if !connect || temp || create {
		t.Fatalf("CONNECT=%v TEMP=%v CREATE=%v; yalnızca CONNECT bekleniyordu", connect, temp, create)
	}
}
