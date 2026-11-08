-- name: CreateDevice :exec
INSERT INTO devices (id, user_id, platform)
VALUES (@id, @user_id, @platform);

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = now() WHERE id = @id;
