-- Duyurular (issue #48). Her sorgu tayfa_id ile sınırlıdır (IDOR); üyelik
-- ve izin kontrolü app katmanındadır (access, authz).

-- name: CreateAnnouncement :exec
INSERT INTO announcements (id, tayfa_id, author_id, title, body, client_request_id)
VALUES (@id, @tayfa_id, @author_id, @title, @body, @client_request_id);

-- Tekrar gönderilen yayınlama isteği aynı duyuruyu döndürür.
-- name: GetAnnouncementByClientRequest :one
SELECT id, tayfa_id, deleted_at
FROM announcements
WHERE author_id = @author_id AND client_request_id = @client_request_id;

-- name: GetAnnouncement :one
SELECT id, tayfa_id, author_id, title, body, pinned, created_at, updated_at
FROM announcements
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Yazma işlemleri satırı kilitleyerek okur; silinmiş satır da döner.
-- name: GetAnnouncementForUpdate :one
SELECT id, tayfa_id, author_id, title, body, pinned, created_at, updated_at, deleted_at, deleted_by
FROM announcements
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- Sabitlenenler önce, yeniden eskiye. Sayfa, önceki sayfanın son satırından
-- sonra başlar (pinned, created_at, id azalan).
-- name: ListAnnouncementsPage :many
SELECT id, tayfa_id, author_id, title, body, pinned, created_at, updated_at
FROM announcements
WHERE tayfa_id = @tayfa_id
  AND deleted_at IS NULL
  AND (NOT @has_cursor::boolean
       OR (pinned, created_at, id) < (@after_pinned::boolean, @after_created_at::timestamptz, @after_id::uuid))
ORDER BY pinned DESC, created_at DESC, id DESC
LIMIT @page_size;

-- name: UpdateAnnouncementContent :execrows
UPDATE announcements
SET title = @title, body = @body
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: SetAnnouncementPinned :execrows
UPDATE announcements
SET pinned = @pinned
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Moderatör/yönetici silmesi: içerik gizlenir, geri alınabilir.
-- name: HideAnnouncement :execrows
UPDATE announcements
SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Yazarın kendi silmesi: başlık ve gövde hemen boşaltılır, geri alınamaz
-- (v14 "Silinen içeriği geri alma").
-- name: EraseAnnouncement :execrows
UPDATE announcements
SET deleted_at = @deleted_at, deleted_by = @deleted_by, title = '', body = '', pinned = false
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: RestoreAnnouncement :execrows
UPDATE announcements
SET deleted_at = NULL, deleted_by = NULL
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NOT NULL AND char_length(title) > 0;
