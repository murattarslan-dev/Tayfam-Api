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

-- core'un panel işlemlerinin denetim kaydı (PlatformAuditService). Kimlik
-- core'un olay kimliğidir; aynı olay ikinci kez gelirse bir şey değişmez.
-- name: InsertPlatformAuditLogOnce :execrows
INSERT INTO platform_audit_logs (id, actor_id, action, target_type, target_id, reason, metadata, created_at)
VALUES (@id, @actor_id, @action, sqlc.narg('target_type'), sqlc.narg('target_id'), @reason, @metadata, @created_at)
ON CONFLICT (id) DO NOTHING;

-- Panel kullanıcı araması (issue #104). En fazla 20 sonuç; doğum tarihi,
-- e-posta ve oturum ayrıntısı dönmez.

-- name: SearchUsersByName :many
SELECT u.id, u.display_name, u.ref_code, u.registered_at, u.deleted_at
FROM users u
WHERE lower(u.display_name) LIKE '%' || @pattern::text || '%' ESCAPE '\'
ORDER BY u.created_at DESC, u.id
LIMIT 20;

-- name: SearchUsersByRefCode :many
SELECT u.id, u.display_name, u.ref_code, u.registered_at, u.deleted_at
FROM users u
WHERE u.ref_code = @ref_code
LIMIT 20;

-- name: SearchUsersByEmail :many
SELECT DISTINCT u.id, u.display_name, u.ref_code, u.registered_at, u.deleted_at
FROM auth_identities ai
JOIN users u ON u.id = ai.user_id
WHERE lower(ai.email) = lower(@email::text)
LIMIT 20;

-- name: GetUserForAdmin :one
SELECT u.id, u.display_name, u.ref_code, u.registered_at, u.deleted_at, u.created_at,
       (SELECT count(*) FROM sessions s
        WHERE s.user_id = u.id AND s.revoked_at IS NULL AND s.expires_at > now())::int AS active_sessions
FROM users u
WHERE u.id = @user_id;

-- Kullanıcıyı kilitler (yaptırım ve oturum kapatma aynı kullanıcıda sırayla
-- işlenir).
-- name: LockUserForAdmin :one
SELECT id FROM users WHERE id = @user_id AND deleted_at IS NULL FOR UPDATE;

-- Kullanıcının kaldırılmamış yaptırımı (süresi dolmuş askı dahil).
-- name: GetOpenSanction :one
SELECT id, user_id, type, reason, starts_at, ends_at
FROM platform_sanctions
WHERE user_id = @user_id AND lifted_at IS NULL;

-- Etkin yaptırım: kaldırılmamış ve (ban ya da bitişi gelmemiş askı).
-- name: GetActiveSanctionType :one
SELECT type FROM platform_sanctions
WHERE user_id = @user_id AND lifted_at IS NULL AND (ends_at IS NULL OR ends_at > now());

-- name: ListActiveSanctionTypes :many
SELECT user_id, type FROM platform_sanctions
WHERE user_id = ANY(@user_ids::uuid[]) AND lifted_at IS NULL AND (ends_at IS NULL OR ends_at > now());

-- name: CreateSanction :one
INSERT INTO platform_sanctions (id, user_id, type, reason, starts_at, ends_at, created_by)
VALUES (@id, @user_id, @type, @reason, @starts_at, sqlc.narg('ends_at'), @created_by)
RETURNING id, user_id, type, reason, starts_at, ends_at;

-- name: LiftSanction :execrows
UPDATE platform_sanctions SET lifted_at = @lifted_at, lifted_by = @lifted_by
WHERE id = @id AND lifted_at IS NULL;
