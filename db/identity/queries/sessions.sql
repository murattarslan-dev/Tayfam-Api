-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, device_id, expires_at)
VALUES (@id, @user_id, @device_id, @expires_at);

-- name: GetActiveSession :one
SELECT id, user_id, device_id, expires_at
FROM sessions
WHERE id = @id AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeSession :execrows
UPDATE sessions
SET revoked_at = now(), revoked_reason = @reason
WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions
SET revoked_at = now(), revoked_reason = @reason
WHERE user_id = @user_id AND revoked_at IS NULL;

-- Hesap yönetimi (issue #75).

-- Kullanıcının kendi açık oturumları, cihaz platformuyla. IP ve token
-- dönmez. Son görülme oturumun son yenilenme zamanıdır.
-- name: ListUserSessions :many
SELECT s.id, s.created_at, s.last_refreshed_at, d.platform
FROM sessions s
JOIN devices d ON d.id = s.device_id
WHERE s.user_id = @user_id AND s.revoked_at IS NULL AND s.expires_at > @now
ORDER BY s.last_refreshed_at DESC, s.id DESC
LIMIT 100;

-- Oturum yalnızca sahibi tarafından kapatılır: user_id koşulu başka
-- kullanıcının oturumunu kapatmayı (IDOR) engeller.
-- name: RevokeOwnSession :execrows
UPDATE sessions
SET revoked_at = now(), revoked_reason = @reason
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL;

-- name: RevokeOtherSessions :execrows
UPDATE sessions
SET revoked_at = now(), revoked_reason = 'user_signout_remote'
WHERE user_id = @user_id AND id <> @keep_id AND revoked_at IS NULL;

-- Çağıranın oturumu: açık, süresi dolmamış ve token'daki kullanıcıya ait.
-- created_at, hesap silmede yeniden kimlik doğrulama için girişin
-- (sağlayıcı token'ının doğrulandığı an) zamanıdır; yenileme onu değiştirmez.
-- name: GetCallerSession :one
SELECT id, created_at
FROM sessions
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL AND expires_at > @now;
