package store

import (
	"testing"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// resetFixtures: 3 users with push tokens, a channel, a notification and the
// outbox with a consumer position.
const resetFixtures = `
SET session_replication_role = replica;
INSERT INTO user_profiles (user_id, display_name, source_event_id)
SELECT ('00000000-0000-7000-8000-0000000000a' || g)::uuid, 'Test ' || g, gen_random_uuid() FROM generate_series(1, 3) g;
INSERT INTO push_tokens (session_id, user_id, platform, token)
SELECT gen_random_uuid(), ('00000000-0000-7000-8000-0000000000a' || g)::uuid, 'android', 'tok-' || g FROM generate_series(1, 3) g;
INSERT INTO channels (id, tayfa_id, name, name_normalized)
VALUES (gen_random_uuid(), '00000000-0000-7000-8000-000000000001', 'genel', 'genel');
INSERT INTO notifications (id, user_id, type, target_type, target_id, source_event_id)
VALUES (gen_random_uuid(), '00000000-0000-7000-8000-0000000000a1', 'announcement', 'announcement', gen_random_uuid(), gen_random_uuid());
INSERT INTO domain_events (id, type) VALUES (gen_random_uuid(), 'channel.created');
INSERT INTO event_consumers (consumer_name) VALUES ('core');
SET session_replication_role = origin;
`

// Users with a push token: 3 more reach the threshold, one more is over it.
const (
	resetAtThreshold = `INSERT INTO push_tokens (session_id, user_id, platform, token)
SELECT gen_random_uuid(), gen_random_uuid(), 'ios', 'ek-' || g FROM generate_series(1, 3) g`
	resetOneMore = `INSERT INTO push_tokens (session_id, user_id, platform, token)
VALUES (gen_random_uuid(), gen_random_uuid(), 'android', 'bir-fazla')`
)

// TestPilotReset pins db/chat/reset/pilot-oncesi-sifirlama.sql (issue
// #369, ADR-0046): every table but the migration history is emptied. The
// threshold allows 6 distinct users with a push token or E2E device key and
// refuses the seventh (demo accounts have no device).
func TestPilotReset(t *testing.T) {
	dbtest.CheckReset(t, dbtest.ResetSpec{
		Service:     "chat",
		Migrations:  chatdb.Migrations(),
		Script:      "../../../db/chat/reset/pilot-oncesi-sifirlama.sql",
		OtherScript: "../../../db/identity/reset/pilot-oncesi-sifirlama.sql",
		Fixtures:    resetFixtures,
		Kept:        []string{"goose_db_version"},
		Filled:      []string{"user_profiles", "push_tokens", "channels", "notifications", "domain_events", "event_consumers"},
		AtThreshold: resetAtThreshold,
		OneMore:     resetOneMore,
	})
}
