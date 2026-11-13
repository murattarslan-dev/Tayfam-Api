package store

import (
	"context"
	"maps"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// chat_app's table privileges (db/chat/migrations/00009_app_role.sql,
// ADR-0015). The outbox is append-only; a direct message cannot be edited,
// only erased by its sender or detached from a deleted account. A new table
// or grant must be added here on purpose.
var wantAppPrivileges = map[string]string{
	"channels":                 "DELETE,INSERT,SELECT,UPDATE",
	"channel_members":          "DELETE,INSERT,SELECT",
	"channel_messages":         "DELETE,INSERT,SELECT,UPDATE",
	"channel_reads":            "DELETE,INSERT,SELECT,UPDATE",
	"notifications":            "DELETE,INSERT,SELECT,UPDATE",
	"notification_preferences": "DELETE,INSERT,SELECT,UPDATE",
	"user_profiles":            "INSERT,SELECT,UPDATE",
	"event_consumers":          "INSERT,SELECT,UPDATE",
	"domain_events":            "INSERT,SELECT",
	"dm_conversations":         "DELETE,INSERT,SELECT,UPDATE(accepted_at,declined_at,last_message_at,requested_by,status,user_high_id,user_low_id)",
	"dm_messages":              "DELETE,INSERT,SELECT,UPDATE(deleted_at,sender_id)",
	"dm_conversation_reads":    "DELETE,INSERT,SELECT,UPDATE(last_read_message_id,read_at)",
	// E2E (00010, ADR-0018): key material, envelopes and archive entries
	// cannot be edited; a device keeps its identity, session and number.
	"user_e2e_state":          "DELETE,INSERT,SELECT,UPDATE(history_key_version,last_device_number,rotation_needed,sessions_ended_at)",
	"device_keys":             "DELETE,INSERT,SELECT,UPDATE(kyber_prekey,kyber_prekey_id,kyber_prekey_signature,last_seen_at,paired_at,prekeys_updated_at,signed_prekey,signed_prekey_id,signed_prekey_signature)",
	"device_one_time_prekeys": "DELETE,INSERT,SELECT,UPDATE(created_at)",
	"user_key_events":         "DELETE,INSERT,SELECT",
	"e2e_ended_sessions":      "DELETE,INSERT,SELECT",
	"dm_envelopes":            "DELETE,INSERT,SELECT",
	"dm_archive_entries":      "DELETE,INSERT,SELECT",
	"device_link_requests":    "DELETE,INSERT,SELECT,UPDATE(answer,answered_at,answered_by_device_id,history_key_version)",
	"history_key_shares":      "DELETE,INSERT,SELECT",
	// Push (00011, ADR-0019): a token stays with its session and user; a
	// queued push stays with its notification.
	"push_tokens": "DELETE,INSERT,SELECT,UPDATE(platform,refreshed_at,token)",
	"push_queue":  "DELETE,INSERT,SELECT,UPDATE(attempts,next_attempt_at,session_ids)",
}

func TestAppRolePrivileges(t *testing.T) {
	pool, _ := setup(t)
	if got := dbtest.TablePrivileges(t, pool); !maps.Equal(got, wantAppPrivileges) {
		t.Fatalf("chat_app yetkileri:\n  %v\nbeklenen:\n  %v", got, wantAppPrivileges)
	}
}

// If the service is taken over, its database user cannot erase the outbox,
// empty tables, switch off triggers or change the schema (ADR-0011 hardening
// item 8).
func TestAppRoleCannotBypassGuards(t *testing.T) {
	pool, _ := setup(t)
	for name, sql := range map[string]string{
		"trigger kapatma":              `ALTER TABLE channels DISABLE TRIGGER channels_updated_at`,
		"tüm trigger'lar":              `ALTER TABLE channel_reads DISABLE TRIGGER ALL`,
		"trigger fonksiyonu":           `CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		"replica modu":                 `SET session_replication_role = replica`,
		"mesajları boşaltma":           `TRUNCATE channel_messages`,
		"olayları boşaltma":            `TRUNCATE domain_events`,
		"olayı güncelleme":             `UPDATE domain_events SET type = 'x.y'`,
		"olayı silme":                  `DELETE FROM domain_events`,
		"profil kopyasını silme":       `DELETE FROM user_profiles`,
		"relay konumunu silme":         `DELETE FROM event_consumers`,
		"tablo silme":                  `DROP TABLE channel_messages`,
		"kolon ekleme":                 `ALTER TABLE channels ADD COLUMN x int`,
		"tablo açma":                   `CREATE TABLE x (id int)`,
		"geçici tablo":                 `CREATE TEMP TABLE x (id int)`,
		"sahipliği alma":               `ALTER TABLE channel_messages OWNER TO chat_app`,
		"migration sürümünü okuma":     `SELECT * FROM goose_db_version`,
		"migration sürümü ekleme":      `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`,
		"mesajı taşıma":                `UPDATE dm_messages SET conversation_id = conversation_id`,
		"mesaj zamanı":                 `UPDATE dm_messages SET created_at = now()`,
		"şifreleme türü":               `UPDATE dm_messages SET encryption = 'e2e'`,
		"konuşma zamanı":               `UPDATE dm_conversations SET created_at = now()`,
		"okunma kaydını taşıma":        `UPDATE dm_conversation_reads SET user_id = user_id`,
		"DM mesajlarını boşaltma":      `TRUNCATE dm_messages`,
		"kimlik anahtarını değiştirme": `UPDATE device_keys SET identity_key = identity_key`,
		"kayıt kimliğini değiştirme":   `UPDATE device_keys SET registration_id = registration_id`,
		"cihazı oturuma taşıma":        `UPDATE device_keys SET session_id = session_id`,
		"cihazı kullanıcıya taşıma":    `UPDATE device_keys SET user_id = user_id`,
		"ön anahtarı değiştirme":       `UPDATE device_one_time_prekeys SET public_key = public_key`,
		"zarfı değiştirme":             `UPDATE dm_envelopes SET ciphertext = ciphertext`,
		"zarfı başka cihaza taşıma":    `UPDATE dm_envelopes SET recipient_device_id = recipient_device_id`,
		"arşivi değiştirme":            `UPDATE dm_archive_entries SET ciphertext = ciphertext`,
		"anahtar olayını değiştirme":   `UPDATE user_key_events SET event_type = event_type`,
		"paylaşımı değiştirme":         `UPDATE history_key_shares SET bundle = bundle`,
		"eşleştirmeyi başkasına açma":  `UPDATE device_link_requests SET device_id = device_id`,
		"zarfları boşaltma":            `TRUNCATE dm_envelopes`,
		"push token'ını taşıma":        `UPDATE push_tokens SET user_id = user_id`,
		"push oturumunu değiştirme":    `UPDATE push_tokens SET session_id = session_id`,
		"push işini taşıma":            `UPDATE push_queue SET notification_id = notification_id`,
		"push trigger'ını kapatma":     `ALTER TABLE notifications DISABLE TRIGGER notifications_enqueue_push`,
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
