-- Pilot öncesi sıfırlama: core (docs/runbooks/pilot-oncesi-sifirlama.md).
-- psql değişkenleri: admins (virgülle ayrılmış kullanıcı kimlikleri),
-- mode (COMMIT ya da ROLLBACK). Tekrar çalıştırılabilir.
--
-- Korunanlar: etkinlik kataloğu (catalog_*), engelli alan adları, iş
-- kayıtları, outbox (domain_events, event_consumers), migration sürümü ve
-- yöneticilerin kendi kayıtları (profil, sosyal ayarlar, müsaitlik, garaj,
-- avatar ve garaj fotoğrafları). Geri kalan her tablo boşaltılır.
\set ON_ERROR_STOP on
BEGIN;

-- Koruma trigger'ları (denetim kaydının TRUNCATE yasağı, üyelik ve anket
-- korumaları) yalnızca bu transaction için devre dışı. Süper kullanıcı
-- gerekir; kubectl exec ... psql postgres kullanıcısıyla çalışır.
SET LOCAL session_replication_role = replica;

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
  (SELECT count(*) FROM tayfas) AS tayfa,
  (SELECT count(*) FROM memberships) AS uyelik,
  (SELECT count(*) FROM events) AS etkinlik,
  (SELECT count(*) FROM uploads WHERE status <> 'deleting') AS dosya;

-- Boşaltılacak tablolar: korunan listede olmayan her tablo. Korunan bir
-- tablo boşaltılan birine foreign key ile bağlıysa TRUNCATE hata verir ve
-- transaction geri alınır.
DO $$
DECLARE
  tables text;
BEGIN
  SELECT string_agg(format('%I', c.relname), ', ' ORDER BY c.relname) INTO tables
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
    AND c.relname NOT LIKE 'catalog\_%'
    AND c.relname NOT IN (
      'goose_db_version', 'domain_events', 'event_consumers', 'job_runs',
      'blocked_domains', 'user_profiles', 'social_settings',
      'user_availability', 'garage_vehicles', 'user_platform_bans', 'uploads');
  RAISE NOTICE 'boşaltılan tablolar: %', tables;
  EXECUTE 'TRUNCATE ' || tables;
END $$;

DELETE FROM user_profiles WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM social_settings WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM user_availability WHERE user_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM garage_vehicles WHERE owner_id NOT IN (SELECT user_id FROM keep_users);
DELETE FROM user_platform_bans WHERE user_id NOT IN (SELECT user_id FROM keep_users);

-- Dosyalar silinmez, silme kuyruğuna alınır: core'un medya işi R2'deki
-- nesneyi siler, sonra satırı (ListDeletingUploads, DeleteUploadRow).
-- Yöneticinin bağlı avatarı ve garaj fotoğrafları kalır.
UPDATE uploads SET status = 'deleting', updated_at = now()
WHERE status <> 'deleting'
  AND NOT (user_id IN (SELECT user_id FROM keep_users)
           AND purpose IN ('avatar', 'garage_vehicle')
           AND attached_type IS NOT NULL);

-- Hesap silme akışıyla aynı: olaylar kalır, silinen kullanıcı aktör olmaz.
UPDATE domain_events SET actor_id = NULL
WHERE actor_id IS NOT NULL AND actor_id NOT IN (SELECT user_id FROM keep_users);

SELECT 'sonra' AS an,
  (SELECT count(*) FROM user_profiles) AS profil,
  (SELECT count(*) FROM tayfas) AS tayfa,
  (SELECT count(*) FROM memberships) AS uyelik,
  (SELECT count(*) FROM events) AS etkinlik,
  (SELECT count(*) FROM uploads WHERE status <> 'deleting') AS dosya;

:mode;
