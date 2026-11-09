-- Tayfa kanalları ve kanal mesajları (issue #81). Her kanal sorgusu
-- tayfa_id ile, her mesaj sorgusu tayfa kapsamında doğrulanmış bir
-- channel_id ile çalışır (IDOR). Üyelik ve rol kontrolü uygulamada, core'un
-- cevabıyla yapılır. Mesaj metni burada yalnızca şifreli bayt olarak geçer.

-- Kanallar.

-- name: CreateChannel :one
INSERT INTO channels (id, tayfa_id, name, name_normalized, visibility, slow_mode_seconds, created_by)
VALUES (@id, @tayfa_id, @name, @name_normalized, @visibility, @slow_mode_seconds, @created_by)
RETURNING id, tayfa_id, name, visibility, slow_mode_seconds, created_by, archived_at, created_at, updated_at;

-- name: GetChannel :one
SELECT id, tayfa_id, name, visibility, slow_mode_seconds, created_by, archived_at, created_at, updated_at
FROM channels
WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: GetChannelForUpdate :one
SELECT id, tayfa_id, name, visibility, slow_mode_seconds, created_by, archived_at, created_at, updated_at
FROM channels
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- Tayfanın kanalları ve çağıranın okunmamış sayısı (en fazla max_unread;
-- silinmiş ve kendi mesajları sayılmaz). include_staff false ise yalnızca
-- members kanalları. Arşivlenmişler sonda.
-- name: ListChannels :many
SELECT c.id, c.tayfa_id, c.name, c.visibility, c.slow_mode_seconds, c.created_by, c.archived_at, c.created_at, c.updated_at,
  r.last_read_message_id,
  (SELECT count(*) FROM (
    SELECT 1 FROM channel_messages m
    WHERE m.channel_id = c.id AND m.deleted_at IS NULL
      AND m.author_id IS DISTINCT FROM sqlc.arg(user_id)::uuid
      AND (r.last_read_message_id IS NULL OR m.id > r.last_read_message_id)
    LIMIT sqlc.arg(max_unread)
  ) AS u) AS unread_count
FROM channels c
LEFT JOIN channel_reads r ON r.channel_id = c.id AND r.user_id = sqlc.arg(user_id)::uuid
WHERE c.tayfa_id = @tayfa_id AND (sqlc.arg(include_staff)::boolean OR c.visibility = 'members')
ORDER BY c.archived_at IS NOT NULL, c.name_normalized, c.id;

-- name: UpdateChannel :one
UPDATE channels
SET name = @name, name_normalized = @name_normalized, visibility = @visibility, slow_mode_seconds = @slow_mode_seconds
WHERE tayfa_id = @tayfa_id AND id = @id
RETURNING id, tayfa_id, name, visibility, slow_mode_seconds, created_by, archived_at, created_at, updated_at;

-- archived_at NULL: arşivden çıkar.
-- name: SetChannelArchived :one
UPDATE channels SET archived_at = sqlc.narg(archived_at)
WHERE tayfa_id = @tayfa_id AND id = @id
RETURNING id, tayfa_id, name, visibility, slow_mode_seconds, created_by, archived_at, created_at, updated_at;

-- Mesajlar.

-- Tekrar gönderim: yazarın aynı client_request_id ile daha önce yazdığı
-- mesaj (hangi kanalda olursa olsun; çağıran kanalı karşılaştırır).
-- name: GetMessageByRequest :one
SELECT id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by
FROM channel_messages
WHERE author_id = @author_id AND client_request_id = @client_request_id;

-- Aynı (yazar, client_request_id) varsa satır açılmaz ve sonuç boş döner.
-- name: CreateMessage :one
INSERT INTO channel_messages (id, channel_id, author_id, body_encrypted, key_version, client_request_id)
VALUES (@id, @channel_id, @author_id, @body_encrypted, @key_version, @client_request_id)
ON CONFLICT (author_id, client_request_id) DO NOTHING
RETURNING id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by;

-- En yeni önce; before_id verilirse ondan eskiler.
-- name: ListMessagesBefore :many
SELECT id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by
FROM channel_messages
WHERE channel_id = @channel_id AND (sqlc.narg(before_id)::uuid IS NULL OR id < sqlc.narg(before_id)::uuid)
ORDER BY id DESC
LIMIT @n;

-- En eski önce, after_id'den yeniler: kaçırılan mesajları tamamlama
-- (ADR-0005).
-- name: ListMessagesAfter :many
SELECT id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by
FROM channel_messages
WHERE channel_id = @channel_id AND id > @after_id
ORDER BY id
LIMIT @n;

-- name: GetMessage :one
SELECT id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by
FROM channel_messages
WHERE channel_id = @channel_id AND id = @id;

-- name: GetMessageForUpdate :one
SELECT id, channel_id, author_id, body_encrypted, key_version, created_at, deleted_at, deleted_by
FROM channel_messages
WHERE channel_id = @channel_id AND id = @id
FOR UPDATE;

-- Yazarın kendi silmesi: metin hemen gider, geri alınamaz.
-- name: EraseMessage :execrows
UPDATE channel_messages
SET body_encrypted = NULL, key_version = NULL, deleted_at = COALESCE(deleted_at, @deleted_at), deleted_by = @deleted_by
WHERE channel_id = @channel_id AND id = @id;

-- Moderatör veya yönetici silmesi: metin şifreli kalır, mesaj gizlenir.
-- name: HideMessage :execrows
UPDATE channel_messages
SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE channel_id = @channel_id AND id = @id AND deleted_at IS NULL;

-- name: RestoreMessage :execrows
UPDATE channel_messages
SET deleted_at = NULL, deleted_by = NULL
WHERE channel_id = @channel_id AND id = @id AND deleted_at IS NOT NULL AND body_encrypted IS NOT NULL;

-- Okunma bilgisi.

-- Okundu işareti yalnızca ileri gider (UUIDv7 zaman sıralı).
-- name: MarkChannelRead :exec
INSERT INTO channel_reads (channel_id, user_id, last_read_message_id)
VALUES (@channel_id, @user_id, @message_id)
ON CONFLICT (channel_id, user_id) DO UPDATE
SET last_read_message_id = GREATEST(channel_reads.last_read_message_id, EXCLUDED.last_read_message_id);

-- name: GetChannelRead :one
SELECT last_read_message_id FROM channel_reads WHERE channel_id = @channel_id AND user_id = @user_id;

-- Saklama ve silme.

-- 30 gün: silinmiş mesajlardan bir parti kalıcı silinir.
-- name: PurgeDeletedMessages :execrows
DELETE FROM channel_messages
WHERE id IN (
  SELECT o.id FROM channel_messages o
  WHERE o.deleted_at < sqlc.arg(before)::timestamptz
  ORDER BY o.deleted_at
  LIMIT sqlc.arg(n)
);

-- Hesap silindi (user.deleted): içerik kalır, yazar "Silinmiş kullanıcı".
-- name: ClearChannelMessageAuthor :exec
UPDATE channel_messages SET author_id = NULL WHERE author_id = @user_id;

-- name: ClearChannelMessageDeletedBy :exec
UPDATE channel_messages SET deleted_by = NULL WHERE deleted_by = @user_id;

-- name: ClearChannelCreator :exec
UPDATE channels SET created_by = NULL WHERE created_by = @user_id;

-- name: DeleteUserChannelReads :exec
DELETE FROM channel_reads WHERE user_id = @user_id;

-- Tayfa kalıcı silindi (tayfa.purged): mesajlar ve okunma kayıtları CASCADE.
-- name: DeleteTayfaChannels :exec
DELETE FROM channels WHERE tayfa_id = @tayfa_id;
