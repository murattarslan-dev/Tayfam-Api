-- Pilot öncesi sıfırlama: identity (docs/runbooks/pilot-oncesi-sifirlama.md).
-- psql değişkeni: mode (COMMIT ya da ROLLBACK). Korunacaklar doğrudan
-- platform_admins'ten okunur. Tekrar çalıştırılabilir.
--
-- Koruma trigger'ları açık kalır: kullanıcı satırı silinince oturumlar,
-- cihazlar, yenileme token'ları, kimlikler, onaylar ve profil ayrıntıları
-- foreign key cascade ile gider. platform_audit_logs'a dokunulmaz
-- (yalnızca eklenir, ADR-0008).
\set ON_ERROR_STOP on
BEGIN;

CREATE TEMP TABLE keep_users ON COMMIT DROP AS
  SELECT user_id FROM platform_admins;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM keep_users) THEN
    RAISE EXCEPTION 'platform_admins boş; hiçbir şey silinmedi';
  END IF;
END $$;

SELECT 'önce' AS an,
  (SELECT count(*) FROM users) AS kullanici,
  (SELECT count(*) FROM users WHERE id IN (SELECT user_id FROM keep_users)) AS korunan,
  (SELECT count(*) FROM sessions) AS oturum;

-- Hesap silme akışıyla aynı (db/identity/queries/domain_events.sql):
-- silinenin görünen adını taşıyan profil olayları gider, diğer olaylarda
-- aktör boşalır.
DELETE FROM domain_events
WHERE type = 'user.profile_updated' AND subject_type = 'user'
  AND subject_id NOT IN (SELECT user_id FROM keep_users);
UPDATE domain_events SET actor_id = NULL
WHERE actor_id IS NOT NULL AND actor_id NOT IN (SELECT user_id FROM keep_users);

-- Dışa aktarma dosyaları core'da silme kuyruğuna alındı; kayıtları da gider.
DELETE FROM data_export_requests;

DELETE FROM users WHERE id NOT IN (SELECT user_id FROM keep_users);

SELECT 'sonra' AS an,
  (SELECT count(*) FROM users) AS kullanici,
  (SELECT count(*) FROM users WHERE id IN (SELECT user_id FROM keep_users)) AS korunan,
  (SELECT count(*) FROM sessions) AS oturum;

:mode;
