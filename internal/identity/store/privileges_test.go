package store

import (
	"context"
	"maps"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// identity_app's table privileges (db/identity/migrations/00004_app_role.sql,
// ADR-0014). identity deletes no rows, so no table has DELETE. A new table
// or grant must be added here on purpose.
var wantAppPrivileges = map[string]string{
	"users":           "INSERT,SELECT,UPDATE",
	"auth_identities": "INSERT,SELECT,UPDATE",
	"devices":         "INSERT,SELECT,UPDATE",
	"sessions":        "INSERT,SELECT,UPDATE",
	"refresh_tokens":  "INSERT,SELECT,UPDATE",
	"consent_records": "INSERT,SELECT",
}

func TestAppRolePrivileges(t *testing.T) {
	pool, _ := setup(t)
	if got := dbtest.TablePrivileges(t, pool); !maps.Equal(got, wantAppPrivileges) {
		t.Fatalf("identity_app yetkileri:\n  %v\nbeklenen:\n  %v", got, wantAppPrivileges)
	}
}

// If the service is taken over, its database user cannot delete accounts or
// consent records wholesale, switch off triggers or change the schema
// (ADR-0011 hardening item 8).
func TestAppRoleCannotBypassGuards(t *testing.T) {
	pool, _ := setup(t)
	for name, sql := range map[string]string{
		"trigger kapatma":          `ALTER TABLE users DISABLE TRIGGER users_updated_at`,
		"replica modu":             `SET session_replication_role = replica`,
		"kullanıcıları boşaltma":   `TRUNCATE users CASCADE`,
		"kullanıcı silme":          `DELETE FROM users`,
		"oturum silme":             `DELETE FROM sessions`,
		"onayı değiştirme":         `UPDATE consent_records SET document_version = '2026-01-01'`,
		"onayı silme":              `DELETE FROM consent_records`,
		"tablo silme":              `DROP TABLE refresh_tokens`,
		"kısıtı kaldırma":          `ALTER TABLE users DROP CONSTRAINT users_age_blocked`,
		"tablo açma":               `CREATE TABLE x (id int)`,
		"geçici tablo":             `CREATE TEMP TABLE x (id int)`,
		"migration sürümünü okuma": `SELECT * FROM goose_db_version`,
		"migration sürümü ekleme":  `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`,
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
