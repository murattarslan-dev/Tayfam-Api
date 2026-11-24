-- Pilot öncesi sıfırlama: chat (docs/runbooks/pilot-oncesi-sifirlama.md).
-- psql değişkenleri: admins (virgülle ayrılmış kullanıcı kimlikleri),
-- mode (COMMIT ya da ROLLBACK). Tekrar çalıştırılabilir.
\set ON_ERROR_STOP on
BEGIN;

CREATE TEMP TABLE keep_users ON COMMIT DROP AS
  SELECT DISTINCT unnest(string_to_array(:'admins', ','))::uuid AS user_id;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM keep_users) THEN
    RAISE EXCEPTION 'korunacak yönetici yok; hiçbir şey silinmedi';
  END IF;
END $$;

SELECT 'önce' AS an,
  (SELECT count(*) FROM user_profiles) AS profil,
  (SELECT count(*) FROM channels) AS kanal,
  (SELECT count(*) FROM channel_messages) AS kanal_mesaji,
  (SELECT count(*) FROM dm_conversations) AS dm,
  (SELECT count(*) FROM notifications) AS bildirim,
  (SELECT count(*) FROM device_keys) AS cihaz_anahtari;

-- Tayfa kanalları, birebir mesajlar ve bildirimler: hepsi silinen tayfalara
-- ya da silinen kullanıcılara bağlıdır.
TRUNCATE channels, channel_members, channel_messages, channel_reads,
  dm_conversations, dm_messages, dm_envelopes, dm_conversation_reads,
  dm_archive_entries, notifications, push_queue, notification_preferences;

-- Yöneticilerin cihaz anahtarları, E2E durumu, push token'ları ve profili
-- kalır; diğer herkesinki gider (prekey'ler ve bağlama istekleri cascade).
DELETE FROM device_link_requests WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM history_key_shares WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM device_keys WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM user_key_events WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM user_e2e_state WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM push_tokens WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM user_profiles WHERE user_id NOT IN (SELECT user_id FROM keep_users);

-- Hesap silme akışıyla aynı: olaylar kalır, silinen kullanıcı aktör olmaz.
UPDATE domain_events SET actor_id = NULL
WHERE actor_id IS NOT NULL AND actor_id NOT IN (SELECT user_id FROM keep_users);

SELECT 'sonra' AS an,
  (SELECT count(*) FROM user_profiles) AS profil,
  (SELECT count(*) FROM channels) AS kanal,
  (SELECT count(*) FROM channel_messages) AS kanal_mesaji,
  (SELECT count(*) FROM dm_conversations) AS dm,
  (SELECT count(*) FROM notifications) AS bildirim,
  (SELECT count(*) FROM device_keys) AS cihaz_anahtari;

:mode;
