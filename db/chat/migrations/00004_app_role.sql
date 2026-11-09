-- Çalışma zamanı kullanıcısı chat_app (ADR-0014, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan chat kullanıcısı
-- çalıştırır. Servis chat_app ile bağlanır; sahip olmadığı için DDL yapamaz,
-- trigger kapatamaz, tabloları boşaltamaz veya silemez. Tablolarda yalnızca
-- sorgularının (db/chat/queries) gerektirdiği DML'e sahiptir: kalıcı
-- silinen mesajlar ve eski bildirimler, hesap ve tayfa silmede temizlenen
-- satırlar dahil. TRUNCATE, REFERENCES ve TRIGGER hiçbir tabloda verilmez;
-- goose_db_version'a hiçbir yetkisi yoktur.
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
