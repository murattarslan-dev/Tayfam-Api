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

-- Yeniden eskiye; aynı created_at'te id ikincil anahtardır (issue #93).
-- name: ListInvites :many
SELECT id, created_by, expires_at, max_uses, use_count, revoked_at, created_at
FROM invites
WHERE tayfa_id = @tayfa_id
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: GetInvite :one
SELECT id, tayfa_id, created_by, expires_at, max_uses, use_count, revoked_at, created_at
FROM invites
WHERE tayfa_id = @tayfa_id AND id = @id;

-- Davet önizlemesi: kilitlemeden okur, link kullanılmaz. Silinmiş tayfanın
-- daveti bulunmaz.
-- name: GetInviteByHash :one
SELECT i.id, i.tayfa_id, i.created_by, i.expires_at, i.max_uses, i.use_count,
       i.revoked_at, t.name, t.status, t.suspended_at, t.deletion_requested_at
FROM invites i
JOIN tayfas t ON t.id = i.tayfa_id
WHERE i.token_hash = @token_hash AND t.deleted_at IS NULL;

-- Geçerli davetler (iptal edilmemiş, süresi dolmamış, tükenmemiş), yeniden
-- eskiye. created_by verilirse yalnızca o kişinin açtıkları.
-- name: ListLiveInvites :many
SELECT id, created_by, expires_at, max_uses, use_count, revoked_at, created_at
FROM invites
WHERE tayfa_id = @tayfa_id
  AND revoked_at IS NULL
  AND expires_at > now()
  AND (max_uses IS NULL OR use_count < max_uses)
  AND (sqlc.narg(created_by)::uuid IS NULL OR created_by = sqlc.narg(created_by)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT 100;
