-- name: CreateInvite :exec
INSERT INTO invites (id, tayfa_id, token_hash, created_by, expires_at, max_uses)
VALUES (@id, @tayfa_id, @token_hash, @created_by, @expires_at, @max_uses);

-- Kullanımda davet kilitlenir; süre, iptal ve kullanım sınırı aynı
-- transaction'da kontrol edilir, eşzamanlı son kullanımdan yalnızca biri
-- ilerler.
-- name: LockInviteByHash :one
SELECT id, tayfa_id, created_by, expires_at, max_uses, use_count, revoked_at
FROM invites
WHERE token_hash = @token_hash
FOR UPDATE;

-- Davet geçerliyse kullanımı sayar; 0 satır: süresi dolmuş, iptal ya da
-- tükenmiş.
-- name: IncrementInviteUse :execrows
UPDATE invites SET use_count = use_count + 1
WHERE id = @id
  AND revoked_at IS NULL
  AND expires_at > now()
  AND (max_uses IS NULL OR use_count < max_uses);

-- name: RevokeInvite :execrows
UPDATE invites SET revoked_at = now()
WHERE tayfa_id = @tayfa_id AND id = @id AND revoked_at IS NULL;

-- name: ListInvites :many
SELECT id, created_by, expires_at, max_uses, use_count, revoked_at, created_at
FROM invites
WHERE tayfa_id = @tayfa_id
ORDER BY created_at DESC
LIMIT @page_size;
