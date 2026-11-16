-- Etkinlik fotoğraf albümü (issue #206, ADR-0028; internal/core/app/event).
-- Yetki, pencere ve sınırlar uygulama katmanında; trigger'lar ikinci hat
-- (00023_event_photos.sql).

-- name: GetEventPhotoByClientRequest :one
SELECT id, event_id, deleted_at FROM event_photos
WHERE uploader_id = @uploader_id AND client_request_id = @client_request_id;

-- Kişinin ve albümün silinmemiş fotoğrafları (sınırlar). Çağıran etkinlik
-- satırını önce kilitler.
-- name: CountEventPhotos :one
SELECT count(*) FILTER (WHERE uploader_id = @uploader_id)::bigint AS own,
       count(*)::bigint AS total
FROM event_photos
WHERE event_id = @event_id AND deleted_at IS NULL;

-- name: CreateEventPhoto :exec
INSERT INTO event_photos (id, event_id, uploader_id, upload_id, caption, client_request_id, created_at)
VALUES (@id, @event_id, @uploader_id, @upload_id, sqlc.narg(caption), @client_request_id, @created_at);

-- name: GetEventPhoto :one
SELECT id, event_id, uploader_id, upload_id, caption, created_at
FROM event_photos
WHERE event_id = @event_id AND id = @id AND deleted_at IS NULL;

-- (created_at, id) ile sayfalanır, eskiden yeniye.
-- name: ListEventPhotos :many
SELECT id, event_id, uploader_id, upload_id, caption, created_at
FROM event_photos
WHERE event_id = @event_id
  AND deleted_at IS NULL
  AND (created_at, id) > (@after_time::timestamptz, @after_id::uuid)
ORDER BY created_at, id
LIMIT @page_size;

-- name: DeleteEventPhoto :execrows
UPDATE event_photos SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE event_id = @event_id AND id = @id AND deleted_at IS NULL;
