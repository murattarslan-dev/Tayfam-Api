-- Çalışma zamanı kullanıcısı core_app (ADR-0014, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan core kullanıcısı
-- çalıştırır. Servis core_app ile bağlanır; sahip olmadığı için DDL yapamaz,
-- trigger kapatamaz (audit_logs ve memberships korumaları dahil), tabloları
-- silemez. Tablolarda yalnızca sorgularının (db/core/queries) gerektirdiği
-- DML'e sahiptir; TRUNCATE hiçbir tabloda verilmez. goose_db_version'a
-- hiçbir yetkisi yoktur.
--
-- Satır kilitleri (FOR UPDATE, FOR NO KEY UPDATE; memberships trigger'ı
-- tayfas satırını kilitler) UPDATE yetkisi ister; kilitlenen tablolarda
-- UPDATE zaten gereklidir. Foreign key kontrolleri ve ON DELETE CASCADE
-- tablo sahibinin yetkisiyle çalışır; örneğin tayfanın kalıcı silinmesi
-- audit_logs'ta DELETE yetkisi gerektirmez.
--
-- core_app rolünü cluster'da CloudNativePG oluşturur
-- (deploy/base/postgres.yaml), yerelde scripts/postgres-init.sql, testlerde
-- dbtest. Rol yoksa bu migration hata verir ve servis başlamaz.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO core_app', current_database());
END
$$;
-- +goose StatementEnd
GRANT USAGE ON SCHEMA public TO core_app;

-- Tayfa kalıcı olarak servis tarafından silinmez (soft delete; 30 günlük
-- kalıcı silme işi geldiğinde DELETE kendi migration'ıyla verilir).
GRANT SELECT, INSERT, UPDATE ON tayfas TO core_app;
GRANT SELECT, INSERT, DELETE ON tayfa_interests TO core_app;
-- Modül ayarı ON CONFLICT DO UPDATE ile yazılır.
GRANT SELECT, INSERT, UPDATE ON tayfa_modules TO core_app;
GRANT SELECT, INSERT, UPDATE ON invites TO core_app;
-- Değişikliklerin kuralları memberships_guard trigger'ındadır.
GRANT SELECT, INSERT, UPDATE, DELETE ON memberships TO core_app;
-- Denetim kaydı ve outbox yalnızca eklenir ve okunur: güncelleme ve silme
-- yetki düzeyinde de yoktur, trigger'a kalmaz.
GRANT SELECT, INSERT ON audit_logs TO core_app;
GRANT SELECT, INSERT ON domain_events TO core_app;
-- Outbox relay'inin hedef başına konumu (ADR-0002); satır silinmez.
GRANT SELECT, INSERT, UPDATE ON event_consumers TO core_app;

-- Sonraki migration'larda açılan tablolar varsayılan olarak yalnızca okunur
-- ve eklenir; UPDATE ve DELETE gereken tablo bunu kendi migration'ında açıkça
-- verir. Sıra (sequence) ve fonksiyonlar için varsayılan yetki verilmez.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO core_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM core_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM core_app;
REVOKE USAGE ON SCHEMA public FROM core_app;
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM core_app', current_database());
END
$$;
-- +goose StatementEnd
