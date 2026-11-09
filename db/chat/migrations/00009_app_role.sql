-- Çalışma zamanı kullanıcısı chat_app (ADR-0015, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan chat kullanıcısı
-- çalıştırır. Servis chat_app ile bağlanır; sahip olmadığı için DDL yapamaz,
-- trigger kapatamaz, tabloları boşaltamaz veya silemez. Tablolarda yalnızca
-- sorgularının (db/chat/queries) gerektirdiği DML'e sahiptir: kalıcı
-- silinen mesajlar ve eski bildirimler, hesap ve tayfa silmede temizlenen
-- satırlar dahil. TRUNCATE, REFERENCES ve TRIGGER hiçbir tabloda verilmez;
-- goose_db_version'a hiçbir yetkisi yoktur.
--
-- Tablolar 00001–00008'dekilerdir; 00004'ün açtığı realtime_tickets 00005'te
-- kaldırıldı (bağlantı erişim token'ıyla açılır, ADR-0014) ve yetki almaz.
--
-- 00001 veritabanında PUBLIC'in CONNECT/TEMP ve şemadaki varsayılan
-- yetkilerini kaldırır; chat_app'e yalnızca CONNECT ve şemada USAGE verilir.
--
-- chat_app rolünü cluster'da CloudNativePG oluşturur
-- (deploy/base/postgres.yaml), yerelde scripts/postgres-init.sql, testlerde
-- dbtest. Rol yoksa bu migration hata verir ve servis başlamaz.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO chat_app', current_database());
END
$$;
-- +goose StatementEnd
GRANT USAGE ON SCHEMA public TO chat_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON
  channels, channel_messages, channel_reads, notifications, notification_preferences
  TO chat_app;
-- Profil kopyası ON CONFLICT DO UPDATE ile yazılır; hesap silmede satır
-- silinmez, isimsiz hale getirilir (tombstone).
GRANT SELECT, INSERT, UPDATE ON user_profiles TO chat_app;
-- Outbox relay'inin hedef başına konumu (ADR-0002); satır silinmez.
GRANT SELECT, INSERT, UPDATE ON event_consumers TO chat_app;
-- Outbox yalnızca eklenir ve okunur.
GRANT SELECT, INSERT ON domain_events TO chat_app;

-- Birebir mesajlar (00006, 00007). Satır kilidi (LockConversationByPair)
-- en az bir kolonda UPDATE ister.
--   dm_conversations: durum (istek, kabul, ret, reddedilenlerden çıkarma),
--     son mesaj zamanı ve hesap silmede tarafın/isteyenin NULL'lanması
--     güncellenir; iki tarafı da silinmiş konuşma silinir. Kimlik ve
--     oluşturma zamanı (günlük istek sınırı buna göre sayılır) değişmez.
--   dm_messages: mesaj düzenlenemez. Yalnızca gönderenin silmesi (metin ve
--     anahtar sürümü boşalır, deleted_at yazılır) ve hesap silmede
--     sender_id'nin NULL'lanması güncellenir; konuşma, şifreleme türü,
--     client_request_id ve zaman değişmez. 30 gün sonra kalıcı silinir.
--   dm_conversation_reads: okundu işareti ileri gider (ON CONFLICT DO
--     UPDATE); hesap silmede satırlar silinir.
GRANT SELECT, INSERT, DELETE ON dm_conversations, dm_messages, dm_conversation_reads TO chat_app;
GRANT UPDATE (user_low_id, user_high_id, status, requested_by, accepted_at, declined_at, last_message_at)
  ON dm_conversations TO chat_app;
GRANT UPDATE (sender_id, body_encrypted, key_version, deleted_at) ON dm_messages TO chat_app;
GRANT UPDATE (last_read_message_id, read_at) ON dm_conversation_reads TO chat_app;

-- Sonraki migration'larda açılan tablolar varsayılan olarak yalnızca okunur
-- ve eklenir; UPDATE ve DELETE gereken tablo bunu kendi migration'ında açıkça
-- verir. Sıra (sequence) ve fonksiyonlar için varsayılan yetki verilmez.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO chat_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM chat_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM chat_app;
REVOKE USAGE ON SCHEMA public FROM chat_app;
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM chat_app', current_database());
END
$$;
-- +goose StatementEnd
