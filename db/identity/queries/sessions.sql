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
