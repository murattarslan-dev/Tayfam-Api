-- Gerçek zamanlı bağlantı biletleri (issue #82). Yalnızca biletin SHA-256
-- özeti tutulur.

-- name: CountLiveRealtimeTickets :one
SELECT count(*) FROM realtime_tickets WHERE user_id = @user_id AND expires_at > @now;

-- name: CreateRealtimeTicket :exec
INSERT INTO realtime_tickets (id, token_hash, user_id, session_id, created_at, expires_at)
VALUES (@id, @token_hash, @user_id, @session_id, @created_at, @expires_at);

-- Bilet tek kullanımlıktır: bulunan satır aynı ifadede silinir, aynı bileti
-- ikinci kez (ya da aynı anda iki kez) kullanan bulamaz. Süresi dolmuş
-- bilet de silinir ama döndürülmez.
-- name: RedeemRealtimeTicket :one
WITH t AS (
  DELETE FROM realtime_tickets WHERE token_hash = @token_hash
  RETURNING user_id, session_id, expires_at
)
SELECT user_id, session_id FROM t WHERE expires_at > @now;

-- name: DeleteExpiredRealtimeTickets :execrows
DELETE FROM realtime_tickets WHERE expires_at <= @now;

-- name: DeleteSessionRealtimeTickets :exec
DELETE FROM realtime_tickets WHERE session_id = @session_id;

-- name: DeleteUserRealtimeTickets :exec
DELETE FROM realtime_tickets WHERE user_id = @user_id;
