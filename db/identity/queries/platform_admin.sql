-- Platform yönetim paneli (issue #104, ADR-0008). platform_admins bu
-- dosyada yalnızca okunur: uygulamada kayıt ekleyen, değiştiren veya silen
-- sorgu yoktur (store testi TestNoPlatformAdminWrites bunu sabitler).

-- Panel girişinde sağlayıcı kimliğinin sahibi, platform yöneticisiyse.
-- Silinmiş, kaydı tamamlanmamış veya yaş nedeniyle reddedilmiş hesap
-- yönetici sayılmaz.
-- name: GetPlatformAdminByIdentity :one
SELECT u.id, u.display_name
FROM auth_identities ai
JOIN users u ON u.id = ai.user_id
JOIN platform_admins pa ON pa.user_id = u.id
WHERE ai.provider = @provider AND ai.subject = @subject
  AND u.deleted_at IS NULL AND u.registered_at IS NOT NULL AND u.age_blocked_at IS NULL;

-- Admin token'ı taşıyan her istekte: kullanıcı hâlâ yönetici mi? Satır
-- silinince bütün admin token'ları bir sonraki istekte geçersiz olur.
-- name: GetPlatformAdmin :one
SELECT u.id, u.display_name
FROM platform_admins pa
JOIN users u ON u.id = pa.user_id
WHERE pa.user_id = @user_id
  AND u.deleted_at IS NULL AND u.registered_at IS NOT NULL AND u.age_blocked_at IS NULL;

-- Yalnızca ekleme (00006 trigger'ı).
-- name: InsertPlatformAuditLog :exec
INSERT INTO platform_audit_logs (id, actor_id, action, target_type, target_id, reason, metadata)
VALUES (@id, @actor_id, @action, sqlc.narg('target_type'), sqlc.narg('target_id'), @reason, @metadata);
