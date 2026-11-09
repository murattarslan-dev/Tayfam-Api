-- Çalışma zamanı kullanıcısı identity_app (ADR-0014, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan identity
-- kullanıcısı çalıştırır. Servis identity_app ile bağlanır; sahip olmadığı
-- için DDL yapamaz, trigger kapatamaz, tabloları silemez. Tablolarda yalnızca
-- sorgularının (db/identity/queries) gerektirdiği DML'e sahiptir: identity'de
-- satır silen sorgu yoktur, bu yüzden DELETE ve TRUNCATE hiçbir tabloda
-- verilmez. goose_db_version'a hiçbir yetkisi yoktur.
--
-- identity_app rolünü cluster'da CloudNativePG oluşturur
-- (deploy/base/postgres.yaml), yerelde scripts/postgres-init.sql, testlerde
-- dbtest. Rol yoksa bu migration hata verir ve servis başlamaz.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO identity_app', current_database());
END
$$;
-- +goose StatementEnd
GRANT USAGE ON SCHEMA public TO identity_app;

GRANT SELECT, INSERT, UPDATE ON users, auth_identities, devices, sessions, refresh_tokens TO identity_app;
-- Onay kayıtları yalnızca eklenir (00002).
GRANT SELECT, INSERT ON consent_records TO identity_app;

-- Sonraki migration'larda açılan tablolar varsayılan olarak yalnızca okunur
-- ve eklenir; UPDATE ve DELETE gereken tablo bunu kendi migration'ında açıkça
-- verir. Sıra (sequence) ve fonksiyonlar için varsayılan yetki verilmez.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO identity_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM identity_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM identity_app;
REVOKE USAGE ON SCHEMA public FROM identity_app;
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM identity_app', current_database());
END
$$;
-- +goose StatementEnd
