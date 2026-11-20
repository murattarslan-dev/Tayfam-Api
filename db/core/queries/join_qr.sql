-- QR ile katılım (issue #264, ADR-0038 §3). Token yalnızca SHA-256 özeti
-- olarak saklanır; hiçbir sorgu token'ın kendisini görmez.

-- name: CreateJoinQRToken :exec
INSERT INTO event_join_qr_tokens (id, event_id, created_by, token_hash, created_at, expires_at)
VALUES (@id, @event_id, @created_by, @token_hash, @created_at, @expires_at);

-- Token özetinden etkinlik ve tayfa. Silinmiş etkinliğin token'ı bulunmaz.
-- name: GetJoinQRToken :one
SELECT k.id, k.event_id, k.created_by, k.expires_at, e.tayfa_id, e.created_by AS organizer_id
FROM event_join_qr_tokens AS k
JOIN events AS e ON e.id = k.event_id
WHERE k.token_hash = @token_hash AND e.deleted_at IS NULL;

-- Bu transaction'daki üyelik eklemesine QR token'ını bildirir;
-- memberships_guard anlık tayfada onaysız katılımı buna göre doğrular.
-- name: SetJoinQRToken :exec
SELECT set_config('app.join_qr_token', @token_id::text, true);

-- name: GetJoinQRCount :one
SELECT COALESCE((SELECT c.joins FROM event_join_qr_counts AS c WHERE c.event_id = @event_id), 0)::integer;

-- Sınır doluysa satır değişmez (0 döner).
-- name: AddJoinQRCount :execrows
INSERT INTO event_join_qr_counts AS c (event_id, joins) VALUES (@event_id, 1)
ON CONFLICT (event_id) DO UPDATE SET joins = c.joins + 1
WHERE c.joins < @max_joins::integer;

-- Katılımcının silinmiş ya da platform banlı olması (ADR-0038 §3).
-- name: JoinQRUserGone :one
SELECT EXISTS (
  SELECT 1 FROM user_profiles AS p WHERE p.user_id = @user_id AND p.deleted_at IS NOT NULL
  UNION ALL
  SELECT 1 FROM user_platform_bans AS b WHERE b.user_id = @user_id AND b.banned_at IS NOT NULL
);

-- 1 günden eski token'lar (ADR-0038 §5; retention işi).
-- name: DeleteOldJoinQRTokens :execrows
DELETE FROM event_join_qr_tokens WHERE created_at < @created_before;

-- Hesap silme (user.deleted): kişinin açtığı token'lar.
-- name: DeleteUserJoinQRTokens :exec
DELETE FROM event_join_qr_tokens WHERE created_by = @user_id;
