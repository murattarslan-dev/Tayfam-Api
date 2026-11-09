-- Uygulama içi bildirimler (issue #64). Alıcıları core seçer (ADR-0009);
-- chat yalnızca yazar. Kullanıcı uçlarındaki her okuma ve yazma çağıranın
-- user_id'siyle sınırlıdır: başkasının bildirimi "yok" sayılır (IDOR).

-- Bir isteğin bildirimleri; aynı olay ikinci kez gelince kullanıcı başına
-- yeni satır açılmaz. Hesabı silinmiş kullanıcıya (profil mezar taşı) satır
-- açılmaz.
-- name: CreateNotifications :execrows
INSERT INTO notifications (id, user_id, tayfa_id, type, actor_id, target_type, target_id, payload, source_event_id)
SELECT n.id, n.user_id, sqlc.narg(tayfa_id), @type, sqlc.narg(actor_id), @target_type, @target_id, @payload, @source_event_id
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@user_ids::uuid[]) AS user_id) AS n
WHERE NOT EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = n.user_id AND p.deleted_at IS NOT NULL)
ON CONFLICT (source_event_id, user_id) DO NOTHING;

-- En yeni önce; after_id verilirse ondan eskiler (UUIDv7 zaman sıralı).
-- name: ListNotifications :many
SELECT id, tayfa_id, type, actor_id, target_type, target_id, payload, read_at, created_at
FROM notifications
WHERE user_id = @user_id AND (sqlc.narg(after_id)::uuid IS NULL OR id < sqlc.narg(after_id)::uuid)
ORDER BY id DESC
LIMIT @n;

-- name: CountUnreadNotifications :one
SELECT count(*) FROM (
  SELECT 1 FROM notifications WHERE user_id = @user_id AND read_at IS NULL LIMIT @max_count
) AS u;

-- Zaten okunmuşsa okunma zamanı değişmez; başkasının bildirimi bulunmaz.
-- name: MarkNotificationRead :one
UPDATE notifications SET read_at = COALESCE(read_at, @now)
WHERE id = @id AND user_id = @user_id
RETURNING id;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications SET read_at = @now
WHERE user_id = @user_id AND read_at IS NULL;

-- 90 gün saklama: en eski satırlardan bir parti.
-- name: DeleteOldNotifications :execrows
DELETE FROM notifications
WHERE id IN (
  SELECT o.id FROM notifications o WHERE o.created_at < sqlc.arg(before)::timestamptz ORDER BY o.created_at LIMIT sqlc.arg(n)
);

-- Hesap silindi.
-- name: DeleteUserNotifications :exec
DELETE FROM notifications WHERE user_id = @user_id;

-- name: ClearNotificationActor :exec
UPDATE notifications SET actor_id = NULL WHERE actor_id = @user_id;

-- name: DeleteUserNotificationPreferences :exec
DELETE FROM notification_preferences WHERE user_id = @user_id;

-- Tercihler.

-- name: ListNotificationPreferences :many
SELECT scope_type, scope_id, level, muted_until, updated_at
FROM notification_preferences
WHERE user_id = @user_id
ORDER BY scope_type, scope_id;

-- name: UpsertNotificationPreference :exec
INSERT INTO notification_preferences (user_id, scope_type, scope_id, level, muted_until)
VALUES (@user_id, @scope_type, @scope_id, @level, sqlc.narg(muted_until))
ON CONFLICT (user_id, scope_type, scope_id) DO UPDATE
SET level = EXCLUDED.level, muted_until = EXCLUDED.muted_until;

-- name: DeleteNotificationPreference :exec
DELETE FROM notification_preferences
WHERE user_id = @user_id AND scope_type = @scope_type AND scope_id = @scope_id;

-- Tayfa kalıcı silindi (tayfa.purged).
-- name: DeleteTayfaNotifications :exec
DELETE FROM notifications WHERE tayfa_id = @tayfa_id;

-- name: DeleteTayfaNotificationPreferences :exec
DELETE FROM notification_preferences WHERE scope_type = 'tayfa' AND scope_id = @tayfa_id;

