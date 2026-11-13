package store

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// identity_app's table privileges (db/identity/migrations/00008_app_role.sql,
// ADR-0015). DELETE is there for account deletion and the retention jobs;
// the outbox gets only the account-deletion exceptions (actor_id, profile
// events). Platform administrators are read-only; the platform audit log
// is append-only except for the retention purge, and a sanction can only be
// lifted. A new table or grant must be added here on purpose.
var wantAppPrivileges = map[string]string{
	"users":                "DELETE,INSERT,SELECT,UPDATE",
	"auth_identities":      "DELETE,INSERT,SELECT,UPDATE",
	"devices":              "DELETE,INSERT,SELECT,UPDATE",
	"display_name_changes": "DELETE,INSERT,SELECT",
	"sessions":             "DELETE,INSERT,SELECT,UPDATE",
	"refresh_tokens":       "DELETE,INSERT,SELECT,UPDATE",
	"ref_code_lookups":     "DELETE,INSERT,SELECT",
	"consent_records":      "INSERT,SELECT",
	// Veri dışa aktarma talepleri (00009, issue #144): durum güncellenir,
	// saklama işi ve hesap silme satırları siler.
	"data_export_requests": "DELETE,INSERT,SELECT,UPDATE",
	"domain_events":        "DELETE,INSERT,SELECT,UPDATE(actor_id)",
	"event_consumers":      "INSERT,SELECT,UPDATE",

	"platform_admins":     "SELECT",
	"platform_audit_logs": "DELETE,INSERT,SELECT",
	"platform_sanctions":  "INSERT,SELECT,UPDATE(lifted_at,lifted_by)",
}

func TestAppRolePrivileges(t *testing.T) {
	pool, _ := setup(t)
	if got := dbtest.TablePrivileges(t, pool); !maps.Equal(got, wantAppPrivileges) {
		t.Fatalf("identity_app yetkileri:\n  %v\nbeklenen:\n  %v", got, wantAppPrivileges)
	}
}

// If the service is taken over, its database user cannot erase consent
// records or the outbox, switch off triggers or change the schema (ADR-0011
// hardening item 8).
func TestAppRoleCannotBypassGuards(t *testing.T) {
	pool, _ := setup(t)
	for name, sql := range map[string]string{
		"trigger kapatma":          `ALTER TABLE users DISABLE TRIGGER users_updated_at`,
		"replica modu":             `SET session_replication_role = replica`,
		"kullanıcıları boşaltma":   `TRUNCATE users CASCADE`,
		"referans trigger'ı":       `ALTER TABLE users DISABLE TRIGGER users_referral_once`,
		"olay trigger'ı":           `ALTER TABLE domain_events DISABLE TRIGGER domain_events_delete_guard`,
		"tüm trigger'lar":          `ALTER TABLE domain_events DISABLE TRIGGER ALL`,
		"trigger fonksiyonu":       `CREATE OR REPLACE FUNCTION domain_events_delete_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN OLD; END $$`,
		"olayları boşaltma":        `TRUNCATE domain_events`,
		"onayları boşaltma":        `TRUNCATE consent_records`,
		"olay türünü değiştirme":   `UPDATE domain_events SET type = 'x.y'`,
		"olay payload'ı":           `UPDATE domain_events SET payload = '{}'`,
		"relay konumunu silme":     `DELETE FROM event_consumers`,
		"onayı değiştirme":         `UPDATE consent_records SET document_version = '2026-01-01'`,
		"onayı silme":              `DELETE FROM consent_records`,
		"tablo silme":              `DROP TABLE refresh_tokens`,
		"kısıtı kaldırma":          `ALTER TABLE users DROP CONSTRAINT users_age_blocked`,
		"tablo açma":               `CREATE TABLE x (id int)`,
		"geçici tablo":             `CREATE TEMP TABLE x (id int)`,
		"migration sürümünü okuma": `SELECT * FROM goose_db_version`,
		"sahipliği alma":           `ALTER TABLE users OWNER TO identity_app`,
		"migration sürümü ekleme":  `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`,
		"yönetici ekleme":          `INSERT INTO platform_admins (user_id) SELECT id FROM users LIMIT 1`,
		"yöneticiyi değiştirme":    `UPDATE platform_admins SET granted_by = NULL`,
		"yöneticiyi silme":         `DELETE FROM platform_admins`,
		"panel kaydını değiştirme": `UPDATE platform_audit_logs SET reason = 'değiştirildi'`,
		"panel kaydını boşaltma":   `TRUNCATE platform_audit_logs`,
		"panel trigger'ı":          `ALTER TABLE platform_audit_logs DISABLE TRIGGER platform_audit_logs_append_only`,
		"yaptırım türü":            `UPDATE platform_sanctions SET type = 'suspend'`,
		"yaptırım gerekçesi":       `UPDATE platform_sanctions SET reason = 'değiştirilmiş gerekçe'`,
		"yaptırım süresi":          `UPDATE platform_sanctions SET ends_at = NULL`,
		"yaptırımı silme":          `DELETE FROM platform_sanctions`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
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

// identity_app may delete events only for the account-deletion cleanup of
// profile copies (DeleteUserProfileEvents); the trigger refuses every other
// type, so a taken-over service cannot drop user.deleted before it is
// delivered.
func TestAppRoleDeletesOnlyProfileEvents(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	user := id.New()
	for _, typ := range []string{"user.profile_updated", "user.deleted"} {
		if _, err := pool.Exec(ctx, `INSERT INTO domain_events (id, type, subject_type, subject_id) VALUES ($1, $2, 'user', $3)`,
			id.New().PG(), typ, user.PG()); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.DeleteUserProfileEvents(ctx, user.PG()); err != nil {
		t.Fatalf("profil olayları silinemedi: %v", err)
	}
	_, err := pool.Exec(ctx, `DELETE FROM domain_events WHERE type = 'user.deleted'`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "TI002" {
		t.Fatalf("user.deleted olayının silinmesi reddedilmeliydi: %v", err)
	}
	var left []string
	rows, err := pool.Query(ctx, `SELECT type FROM domain_events ORDER BY type`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		left = append(left, s)
	}
	if rows.Err() != nil || len(left) != 1 || left[0] != "user.deleted" {
		t.Fatalf("kalan olaylar = %v, %v", left, rows.Err())
	}
}
