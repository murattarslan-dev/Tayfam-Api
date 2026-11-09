package store

import (
	"context"
	"maps"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// core_app's table privileges (db/core/migrations/00003_app_role.sql,
// ADR-0014). A new table or grant must be added here on purpose.
var wantAppPrivileges = map[string]string{
	"tayfas":          "INSERT,SELECT,UPDATE",
	"tayfa_interests": "DELETE,INSERT,SELECT",
	"tayfa_modules":   "INSERT,SELECT,UPDATE",
	"invites":         "INSERT,SELECT,UPDATE",
	"memberships":     "DELETE,INSERT,SELECT,UPDATE",
	"audit_logs":      "INSERT,SELECT",
	"domain_events":   "INSERT,SELECT",
	"event_consumers": "INSERT,SELECT,UPDATE",
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
		"denetim kaydını silme":         `DELETE FROM audit_logs`,
		"olayı güncelleme":              `UPDATE domain_events SET type = 'x.y'`,
		"olayı silme":                   `DELETE FROM domain_events`,
		"tayfayı kalıcı silme":          `DELETE FROM tayfas`,
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
