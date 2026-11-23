-- Günlük bileşeni (`log`; issue #243; ADR-0025 §4). Etkinlik her zaman önce
-- tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde çözülür
-- (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki ve konum
-- görünürlüğü app katmanındadır (internal/core/app/component/logbook).

-- name: GetLogEntryByClientRequest :one
SELECT id, event_id, component_key, deleted_at FROM event_log_entries
WHERE user_id = @user_id AND client_request_id = @client_request_id;

-- name: CountUserLogEntries :one
SELECT count(*) FROM event_log_entries
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id AND deleted_at IS NULL;

-- name: CreateLogEntry :exec
INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, lat, lng, location_visibility,
  photo_upload_id, client_request_id, created_at)
VALUES (@id, @event_id, @component_key, @user_id, @field_values, @lat, @lng, @location_visibility,
  @photo_upload_id, @client_request_id, @created_at);

-- name: GetLogEntry :one
SELECT id, user_id, field_values, lat, lng, location_visibility, photo_upload_id, created_at
FROM event_log_entries
WHERE event_id = @event_id AND component_key = @component_key AND id = @id AND deleted_at IS NULL;

-- (created_at, id) ile sayfalanır.
-- name: ListLogEntries :many
SELECT id, user_id, field_values, lat, lng, location_visibility, photo_upload_id, created_at
FROM event_log_entries
WHERE event_id = @event_id AND component_key = @component_key
  AND deleted_at IS NULL
  AND (created_at, id) > (@after_time::timestamptz, @after_id::uuid)
ORDER BY created_at, id
LIMIT @page_size;

-- name: DeleteLogEntry :execrows
UPDATE event_log_entries SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE event_id = @event_id AND component_key = @component_key AND id = @id AND deleted_at IS NULL;
