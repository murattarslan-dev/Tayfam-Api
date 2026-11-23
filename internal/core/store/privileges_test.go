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
	"social_settings":         "DELETE,INSERT,SELECT,UPDATE(discover_visible_to_friends,hide_from_suggestions,now_from_friends,profiling_enabled,updated_at)",
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
	// Issue #259: the study tag list is catalog data too.
	"catalog_study_tags": "SELECT",
	// Issue #258: badge definitions are catalog data; an earned badge is
	// never changed or taken back (ADR-0039 §2), only one's own go with
	// one's account. Each frozen event is looked at once.
	"catalog_badges":   "SELECT",
	"user_badges":      "DELETE,INSERT,SELECT",
	"tayfa_badges":     "INSERT,SELECT",
	"event_badge_runs": "INSERT,SELECT",
	// Issue #266: statistics are derived; a tayfa's rows are deleted and
	// written again, never updated. A run mark only changes its state.
	"tayfa_stats":      "DELETE,INSERT,SELECT",
	"user_stats":       "DELETE,INSERT,SELECT",
	"event_stat_runs":  "INSERT,SELECT,UPDATE(changed_at,counted)",
	"tayfa_stat_queue": "DELETE,INSERT,SELECT,UPDATE(queued_at)",
	// Issue #268: the home page layout is written as a whole list; a row
	// never moves to another tayfa or widget and is never deleted (it goes
	// with the tayfa).
	"tayfa_home_widgets": "INSERT,SELECT,UPDATE(config,enabled,position,updated_at)",
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
	"event_vehicles":              "DELETE,INSERT,SELECT,UPDATE(color,make,model,model_year,plate,seats,updated_at,vehicle_id)",
	"event_vehicle_seat_requests": "DELETE,INSERT,SELECT,UPDATE(decided_at,status)",
	// Issue #245: a measurement never moves to another event, component or
	// person; only its value changes and account deletion clears its writer.
	"event_measurements": "DELETE,INSERT,SELECT,UPDATE(entered_by,updated_at,value)",
	// Issue #246: a target never moves to another event or component; only
	// its value changes.
	"event_goals": "DELETE,INSERT,SELECT,UPDATE(target,updated_at)",
	// Issue #247: a programme item never moves to another event or
	// component; its values change and it is deleted.
	"event_schedule_items": "DELETE,INSERT,SELECT,UPDATE(ends_at,note,starts_at,title,updated_at)",
	// Issue #248: a team never moves to another event or component; only
	// its name changes when the teams are formed again. A member row never
	// changes: it is deleted and written again.
	"event_teams":        "DELETE,INSERT,SELECT,UPDATE(name,updated_at)",
	"event_team_members": "DELETE,INSERT,SELECT",
	// Issue #249: a draw is fixed. Rows are only written; account deletion
	// empties a person's place.
	"event_draws":        "INSERT,SELECT",
	"event_draw_entries": "INSERT,SELECT,UPDATE(user_id)",
	// Issue #250: score presets are catalog data the service only reads.
	// A table, a seat or a score never moves to another event, component or
	// table; account deletion empties a person's seat.
	"catalog_score_presets": "SELECT",
	"event_score_tables":    "DELETE,INSERT,SELECT,UPDATE(finished_at,label,status,updated_at)",
	"event_score_players":   "DELETE,INSERT,SELECT,UPDATE(user_id)",
	"event_scores":          "DELETE,INSERT,SELECT",
	// Issue #251: a bracket, a match or a game never moves to another
	// event, component or match; a correction deletes the game and writes
	// it again, a reset deletes the bracket.
	"event_brackets":        "DELETE,INSERT,SELECT",
	"event_bracket_matches": "DELETE,INSERT,SELECT",
	"event_bracket_games":   "DELETE,INSERT,SELECT",
	// Issue #252: a guest never moves to another event; only its nickname
	// and who added it are cleared (expiry, account deletion).
	"event_guest_players": "DELETE,INSERT,SELECT,UPDATE(created_by,nickname)",
	// Issue #253: a ranking entry never moves to another event, component
	// or person; only its value changes and account deletion clears its
	// writer.
	"event_ranking_entries": "DELETE,INSERT,SELECT,UPDATE(entered_by,updated_at,value)",
	// Issue #255: an announcement never moves to another event or
	// component and is not deleted; a new announcement touches it and
	// account deletion clears its announcer. The winners chosen by hand are
	// replaced, never changed.
	"event_celebrations":        "INSERT,SELECT,UPDATE(announced_by,updated_at)",
	"event_celebration_winners": "DELETE,INSERT,SELECT",
	// Issue #254: an option never moves or changes its label; a vote
	// changes only its target and time; a closure is never deleted.
	"event_vote_options":  "DELETE,INSERT,SELECT,UPDATE(created_by)",
	"event_votes":         "DELETE,INSERT,SELECT,UPDATE(candidate_user_id,event_vehicle_id,option_id,photo_id,updated_at)",
	"event_vote_closures": "INSERT,SELECT,UPDATE(closed_by)",
	// Issue #256: a question never moves to another event, component or
	// author, and its text and anonymity do not change; only the answered
	// mark and the soft delete are written. A vote is added or deleted.
	"event_questions":      "DELETE,INSERT,SELECT,UPDATE(answered_at,answered_by,deleted_at,deleted_by)",
	"event_question_votes": "DELETE,INSERT,SELECT",
	// Issue #269: a daily item never moves to another tayfa and its
	// request ID never changes; the text, place and pin are edited, the
	// soft delete is written and undone, account deletion clears who added
	// and deleted it. The rotation state only moves.
	"tayfa_daily_items": "DELETE,INSERT,SELECT,UPDATE(attribution,body,created_by,deleted_at,deleted_by,pin_day,pin_month,pin_year,position,updated_at)",
	"tayfa_daily_state": "INSERT,SELECT,UPDATE(next_position,shown_item_id,shown_on,updated_at)",
	// Issue #270: a library item never moves to another tayfa and its
	// kind and request ID never change; a loan never moves to another
	// item or tayfa (only its state, times and, on account deletion, the
	// borrower change); a queue place is added and deleted.
	"library_items": "DELETE,INSERT,SELECT,UPDATE(name,note,owner_id,photo_upload_id,status,updated_at,vocabulary_id,vocabulary_namespace,withdrawn_at,withdrawn_by)",
	"library_loans": "DELETE,INSERT,SELECT,UPDATE(approved_at,borrower_id,due_at,ended_at,handed_over_at,orphaned_at,reminded_at,state)",
	"library_queue": "DELETE,INSERT,SELECT",
	// Issue #261: a material never moves to another tayfa and its kind
	// and request ID never change; the blocked domain list is read only
	// (the platform panel manages it, #262).
	"materials":       "DELETE,INSERT,SELECT,UPDATE(author_id,deleted_at,deleted_by,domain,photo1_upload_id,photo2_upload_id,photo3_upload_id,subject_key,title,topic_key,updated_at,url)",
	"blocked_domains": "SELECT",
	// Issue #272: a garage vehicle never moves to another person and its
	// request ID never changes; a visibility row never moves.
	"garage_vehicles":         "DELETE,INSERT,SELECT,UPDATE(color,deleted_at,deleted_by,make,model,model_year,note,photo1_upload_id,photo2_upload_id,photo3_upload_id,updated_at)",
	"garage_tayfa_visibility": "DELETE,INSERT,SELECT,UPDATE(updated_at,visible)",
	// Issue #271: the pool_link list is replaced as a whole (deleted and
	// written again); a link never moves.
	"event_pool_links": "DELETE,INSERT,SELECT",
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
	// Issue #264, ADR-0038 §3: a join QR token is written once and deleted
	// (a day later, or with its creator's account); the per-event QR join
	// counter only grows.
	"event_join_qr_tokens": "DELETE,INSERT,SELECT",
	"event_join_qr_counts": "INSERT,SELECT,UPDATE(joins)",
	// Issue #265, ADR-0038 §4–§5: a now notification counter only grows;
	// it is deleted after two days or with its user's account.
	"now_notification_counters": "DELETE,INSERT,SELECT,UPDATE(sent)",
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
