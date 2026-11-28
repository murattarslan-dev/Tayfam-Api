package store

import (
	"testing"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// resetFixtures: 3 real accounts, 1 platform administrator, 2 demo accounts
// and 1 deleted account (the threshold counts only the 3), with identities,
// devices, sessions, consents, the panel audit log and the outbox.
const resetFixtures = `
-- 3 gerçek hesap, 1 yönetici, 2 demo hesabı, 1 silinmiş hesap.
INSERT INTO users (id, display_name, locale, registered_at, birth_date_enc)
SELECT gen_random_uuid(), 'Test ' || g, 'tr', now(), decode(repeat('ab', 40), 'hex') FROM generate_series(1, 6) g;
INSERT INTO users (id, display_name, locale, deleted_at) VALUES (gen_random_uuid(), NULL, 'tr', now());
WITH u AS (SELECT id, row_number() OVER (ORDER BY display_name) r FROM users WHERE display_name IS NOT NULL)
INSERT INTO auth_identities (id, user_id, provider, subject, email)
SELECT gen_random_uuid(), id,
       CASE WHEN r > 4 THEN 'demo' ELSE 'google' END,
       CASE WHEN r > 4 THEN 'demo-0' || r ELSE 'g-' || r END,
       CASE WHEN r > 4 THEN 'demo-0' || r || '@demo.tayfam.app' ELSE 'kisi' || r || '@example.com' END
FROM u;
INSERT INTO platform_admins (user_id) SELECT id FROM users WHERE display_name = 'Test 4';
INSERT INTO devices (id, user_id, platform) SELECT gen_random_uuid(), id, 'android' FROM users WHERE display_name IN ('Test 1', 'Test 2');
INSERT INTO sessions (id, user_id, device_id, expires_at) SELECT gen_random_uuid(), user_id, id, now() + interval '1 day' FROM devices;
INSERT INTO consent_records (id, user_id, document_type, document_version, accepted_at)
SELECT gen_random_uuid(), id, 'terms', '2026-11-27', now() FROM users WHERE registered_at IS NOT NULL;
INSERT INTO platform_audit_logs (id, actor_id, action, target_type, target_id, reason)
SELECT gen_random_uuid(), user_id, 'admin.sign_in', NULL, NULL, 'panel girişi testi' FROM platform_admins;
INSERT INTO domain_events (id, type, actor_id, subject_type, subject_id)
SELECT gen_random_uuid(), 'user.profile_updated', id, 'user', id FROM users WHERE display_name = 'Test 1';
INSERT INTO event_consumers (consumer_name, last_event_id) SELECT 'core', id FROM domain_events;
`

// TestPilotReset pins db/identity/reset/pilot-oncesi-sifirlama.sql (issue
// #369, ADR-0046): every user table is emptied, platform_admins included
// (it references users); the catalog copy and the migration history stay.
// The threshold allows 5 real accounts (not demo, not administrator, not
// deleted) and refuses the sixth.
func TestPilotReset(t *testing.T) {
	dbtest.CheckReset(t, dbtest.ResetSpec{
		Service:     "identity",
		Migrations:  identitydb.Migrations(),
		Script:      "../../../db/identity/reset/pilot-oncesi-sifirlama.sql",
		OtherScript: "../../../db/core/reset/pilot-oncesi-sifirlama.sql",
		Fixtures:    resetFixtures,
		Kept:        []string{"catalog_copy", "catalog_interests", "goose_db_version"},
		Filled: []string{
			"users", "auth_identities", "devices", "sessions", "consent_records",
			"platform_admins", "platform_audit_logs", "domain_events", "event_consumers",
		},
		AtThreshold: `INSERT INTO users (id, display_name, locale) SELECT gen_random_uuid(), 'Ek ' || g, 'tr' FROM generate_series(1, 2) g`,
		OneMore:     `INSERT INTO users (id, display_name, locale) VALUES (gen_random_uuid(), 'Bir fazla', 'tr')`,
	})
}
