-- Kullanıcı şikâyeti doğrudan platforma (issue #187; platform sahibinin
-- kararı 2026-11-13, Notion Proje Planı §9 "Kötüye kullanım").
--
-- Bir kullanıcının kendisi (target_type = 'user') hakkındaki şikâyet artık
-- hiçbir tayfanın moderasyon kuyruğuna düşmez; oluşturulduğu anda
-- platforma yükselir (platform_only, escalation_reason = 'user_target'):
--   - Kişi profilinden (POST /v1/me/reports): tayfa_id NULL'dır, ortak
--     tayfa şartı yoktur; hedef, şikâyet edenin profilini görebildiği bir
--     kullanıcıdır. staff_conduct geçerli değildir (tayfa bağlamı yok).
--   - Tayfa ucundan (geriye uyum): tayfa_id platforma bağlam olarak kalır,
--     şikâyet yine yalnızca platforma gider.
-- Bu migration'dan önce açılmış kullanıcı şikâyetleri (platform_only =
-- false) olduğu gibi kalır: tayfa kuyruğunda çözülür ya da 7 gün sonra
-- mevcut işle platforma yükselir. reports_guard platform_only'nin
-- değiştirilmesine izin vermediği için geriye dönük düzeltme yapılmaz.
--
-- Geri alma planı: Down tayfasız ve platforma özel kullanıcı şikâyetlerini
-- siler (yalnızca bu migration'la oluşabilirler) ve kısıtları 00013'teki
-- haline döndürür.

-- +goose Up
ALTER TABLE reports DROP CONSTRAINT reports_tayfa_dm;
ALTER TABLE reports ADD CONSTRAINT reports_tayfa_dm CHECK (
  (target_type <> 'dm_message' OR tayfa_id IS NULL)
  AND (tayfa_id IS NOT NULL OR target_type IN ('dm_message', 'user')));
-- Tayfasız şikâyet her zaman platforma özeldir.
ALTER TABLE reports ADD CONSTRAINT reports_no_tayfa_platform CHECK (tayfa_id IS NOT NULL OR platform_only);
-- Profilden yapılan kullanıcı şikâyetinde tayfa yetkilisi davranışı yok.
ALTER TABLE reports ADD CONSTRAINT reports_user_profile_reason CHECK (
  target_type <> 'user' OR tayfa_id IS NOT NULL OR reason <> 'staff_conduct');

-- Kullanıcı şikâyetinde platform_only hem eski (false) hem yeni (true)
-- satırlarda geçerlidir; uygulama yenilerini her zaman true yazar.
ALTER TABLE reports DROP CONSTRAINT reports_platform_only;
ALTER TABLE reports ADD CONSTRAINT reports_platform_only CHECK (
  target_type = 'user'
  OR platform_only = (target_type IN ('tayfa', 'dm_message') OR reason = 'staff_conduct'));

ALTER TABLE reports DROP CONSTRAINT reports_escalation_reason;
ALTER TABLE reports ADD CONSTRAINT reports_escalation_reason CHECK (escalation_reason IS NULL OR escalation_reason IN
  ('tayfa_target', 'staff_conduct', 'child_safety', 'minor_involved', 'unresolved', 'direct_message', 'user_target'));

-- +goose Down
DELETE FROM reports WHERE target_type = 'user' AND (tayfa_id IS NULL OR (platform_only AND reason <> 'staff_conduct'));
ALTER TABLE reports DROP CONSTRAINT reports_escalation_reason;
ALTER TABLE reports ADD CONSTRAINT reports_escalation_reason CHECK (escalation_reason IS NULL OR escalation_reason IN
  ('tayfa_target', 'staff_conduct', 'child_safety', 'minor_involved', 'unresolved', 'direct_message'));
ALTER TABLE reports DROP CONSTRAINT reports_platform_only;
ALTER TABLE reports ADD CONSTRAINT reports_platform_only CHECK (
  platform_only = (target_type IN ('tayfa', 'dm_message') OR reason = 'staff_conduct'));
ALTER TABLE reports DROP CONSTRAINT reports_user_profile_reason;
ALTER TABLE reports DROP CONSTRAINT reports_no_tayfa_platform;
ALTER TABLE reports DROP CONSTRAINT reports_tayfa_dm;
ALTER TABLE reports ADD CONSTRAINT reports_tayfa_dm CHECK ((tayfa_id IS NULL) = (target_type = 'dm_message'));
