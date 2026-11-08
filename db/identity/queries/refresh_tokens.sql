-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, session_id, token_hash)
VALUES (@id, @session_id, @token_hash);

-- Yenilemede token ve oturumu birlikte kilitler; aynı token'la eşzamanlı iki
-- yenileme isteğinden yalnızca biri ilerler, diğeri yeniden kullanım sayılır.
-- name: LockRefreshToken :one
SELECT rt.id, rt.session_id, rt.used_at,
       s.user_id, s.device_id, s.expires_at, s.revoked_at
FROM refresh_tokens rt
JOIN sessions s ON s.id = rt.session_id
WHERE rt.token_hash = @token_hash
FOR UPDATE OF rt, s;

-- name: MarkRefreshTokenUsed :execrows
UPDATE refresh_tokens SET used_at = now()
WHERE id = @id AND used_at IS NULL;

-- Oturum 90 gün kullanılmazsa sona erer (ADR-0004); her yenilemede süre
-- yeniden başlar.
-- name: ExtendSession :exec
UPDATE sessions SET last_refreshed_at = now(), expires_at = @expires_at
WHERE id = @id AND revoked_at IS NULL;
