-- Çalışma zamanı kullanıcısı identity_app (ADR-0015, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan identity
-- kullanıcısı çalıştırır. Servis identity_app ile bağlanır; sahip olmadığı
-- için DDL yapamaz, trigger kapatamaz, tabloları boşaltamaz veya silemez.
-- Tablolarda yalnızca sorgularının (db/identity/queries) ve saklama işinin
-- (internal/identity/app/retention) gerektirdiği DML'e sahiptir. TRUNCATE,
-- REFERENCES ve TRIGGER hiçbir tabloda verilmez; goose_db_version'a hiçbir
-- yetkisi yoktur.
--
-- Satır kilitleri (FOR UPDATE, FOR NO KEY UPDATE) kilitlenen tabloda UPDATE
-- yetkisi ister. Foreign key kontrolleri ve ON DELETE CASCADE / SET NULL
-- tablo sahibinin yetkisiyle çalışır: kullanıcının kalıcı silinmesi,
-- cihaz ve oturumların cascade ile silinmesi için bağlı tablolarda ayrıca
-- DELETE gerekmez.
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

-- Hesap silme ve saklama işleri (v14 "Saklama süreleri"): 30 gün önce
-- silinmiş hesaplar, 90 gündür görünmeyen cihazlar, kapanmış oturumlar ve
-- kullanılmış refresh token'lar kalıcı silinir; giriş kimlikleri hesap
-- silmede hemen silinir.
GRANT SELECT, INSERT, UPDATE, DELETE ON users, auth_identities, devices, sessions, refresh_tokens TO identity_app;
-- Referans kodu arama denemeleri kendini temizler (00004); satır güncellenmez.
GRANT SELECT, INSERT, DELETE ON ref_code_lookups TO identity_app;
-- Onay kayıtları yalnızca eklenir (00002).
GRANT SELECT, INSERT ON consent_records TO identity_app;
-- Outbox relay'inin hedef başına konumu (ADR-0002); satır silinmez.
GRANT SELECT, INSERT, UPDATE ON event_consumers TO identity_app;

-- Outbox yalnızca eklenir ve okunur. Hesap silmedeki iki dar istisna
-- (userevents, veri modeli v14 "İlişkiler ve Silme Davranışı"):
--   * actor_id NULL yapılır: UPDATE yalnızca bu kolonda verilir; tür,
--     konu ve payload değiştirilemez.
--   * silinen kullanıcının henüz silinmemiş user.profile_updated olayları
--     (görünen adı taşır) silinir. DELETE kolon düzeyinde verilemediği için
--     aşağıdaki trigger başka türdeki olayların silinmesini reddeder:
--     servisi ele geçiren biri user.deleted olaylarını silerek hesap
--     silmenin diğer servislere ulaşmasını engelleyemez.
GRANT SELECT, INSERT, DELETE ON domain_events TO identity_app;
GRANT UPDATE (actor_id) ON domain_events TO identity_app;

-- +goose StatementBegin
CREATE FUNCTION domain_events_delete_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.type = 'user.profile_updated' THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'olay silinemez' USING ERRCODE = 'TI002';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER domain_events_delete_guard BEFORE DELETE ON domain_events
  FOR EACH ROW EXECUTE FUNCTION domain_events_delete_guard();

-- Platform yönetim paneli (00006, 00007; ADR-0008):
--   platform_admins: yalnızca okunur. Kayıt uygulamadan eklenemez,
--     değiştirilemez, silinemez; platform sahibi migration kullanıcısıyla
--     ekler (docs/runbooks/platform-yoneticisi.md). Kullanıcının kalıcı
--     silinmesi satırı ON DELETE CASCADE ile tablo sahibinin yetkisiyle
--     siler.
--   platform_audit_logs: okunur ve eklenir; DELETE yalnızca saklama işi
--     (1 yıl) içindir ve sınırı platform_audit_logs_append_only
--     trigger'ındadır (365 günden yeni satır silinemez, hiçbir satır
--     güncellenemez). InsertPlatformAuditLogOnce'ın ON CONFLICT DO NOTHING'i
--     UPDATE istemez.
--   platform_sanctions: okunur ve eklenir; yalnızca kaldırma kolonları
--     (lifted_at, lifted_by) güncellenir. Tür, gerekçe, süre ve oluşturan
--     değiştirilemez; satır silinmez (kullanıcı silinince CASCADE).
GRANT SELECT ON platform_admins TO identity_app;
GRANT SELECT, INSERT, DELETE ON platform_audit_logs TO identity_app;
GRANT SELECT, INSERT ON platform_sanctions TO identity_app;
GRANT UPDATE (lifted_at, lifted_by) ON platform_sanctions TO identity_app;

-- Sonraki migration'larda açılan tablolar varsayılan olarak yalnızca okunur
-- ve eklenir; UPDATE ve DELETE gereken tablo bunu kendi migration'ında açıkça
-- verir. Sıra (sequence) ve fonksiyonlar için varsayılan yetki verilmez.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO identity_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM identity_app;
DROP TRIGGER domain_events_delete_guard ON domain_events;
DROP FUNCTION domain_events_delete_guard();
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM identity_app;
REVOKE USAGE ON SCHEMA public FROM identity_app;
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM identity_app', current_database());
END
$$;
-- +goose StatementEnd
