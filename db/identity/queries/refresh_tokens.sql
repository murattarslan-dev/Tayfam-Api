-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, session_id, token_hash)
VALUES (@id, @session_id, @token_hash);

-- Yenilemede token ve oturumu birlikte kilitler; aynı token'la eşzamanlı iki
-- yenileme isteğinden yalnızca biri ilerler, diğeri yeniden kullanım sayılır.
-- name: LockRefreshToken :one
SELECT rt.id, rt.session_id, rt.used_at,
       s.user_id, s.device_id, s.expires_at, s.revoked_at,
       s.integrity_status, s.integrity_reason, s.integrity_checked_at, s.integrity_passed_at
FROM refresh_tokens rt
JOIN sessions s ON s.id = rt.session_id
WHERE rt.token_hash = @token_hash
FOR UPDATE OF rt, s;

-- Yenilemeden önce, kilitsiz: oturumun bütünlük denetimi gerekiyor mu ve
-- cihaz hangi platformda (ADR-0022). Google'a transaction dışında gidilir;
-- karar transaction'da kilitli satırla yeniden verilir.
-- name: PeekRefreshIntegrity :one
SELECT s.integrity_status, s.integrity_reason, s.integrity_checked_at, s.integrity_passed_at, d.platform
FROM refresh_tokens rt
JOIN sessions s ON s.id = rt.session_id
JOIN devices d ON d.id = s.device_id
WHERE rt.token_hash = @token_hash AND rt.used_at IS NULL AND s.revoked_at IS NULL;

-- name: MarkRefreshTokenUsed :execrows
UPDATE refresh_tokens SET used_at = now()
WHERE id = @id AND used_at IS NULL;

-- Oturum 90 gün kullanılmazsa sona erer (ADR-0004); her yenilemede süre
-- yeniden başlar.
-- name: ExtendSession :exec
UPDATE sessions SET last_refreshed_at = now(), expires_at = @expires_at
WHERE id = @id AND revoked_at IS NULL;
