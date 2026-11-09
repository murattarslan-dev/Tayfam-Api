-- name: CreateDevice :exec
INSERT INTO devices (id, user_id, platform)
VALUES (@id, @user_id, @platform);

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = now() WHERE id = @id;

-- Hesap silmede cihazlar hemen silinir; oturumlar ve refresh token'lar
-- ON DELETE CASCADE ile gider (veri modeli v14 "İlişkiler ve Silme
-- Davranışı").
-- name: DeleteUserDevices :execrows
DELETE FROM devices WHERE user_id = @user_id;
