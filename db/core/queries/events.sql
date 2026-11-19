-- Etkinlikler (issue #49). Her sorgu tayfa_id ile ya da tayfa kapsamında
-- yüklenmiş bir etkinliğin kimliğiyle çalışır (IDOR); yetki kontrolü app
-- katmanındadır (authz).

-- name: CreateEvent :exec
INSERT INTO events (
  id, tayfa_id, type, type_version, mode, title, description, starts_at, ends_at,
  meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
  capacity, discoverable, created_by, client_request_id
) VALUES (
  @id, @tayfa_id, @type, @type_version, @mode, @title, @description, @starts_at, @ends_at,
  @meeting_point_name, @meeting_lat, @meeting_lng, @city, @district, @online_url,
  @capacity, @discoverable, @created_by, @client_request_id
);

-- Tekrar gönderilen oluşturma isteği aynı etkinliği döndürür.
-- name: GetEventByClientRequest :one
SELECT id, tayfa_id, deleted_at FROM events
WHERE created_by = @created_by AND client_request_id = @client_request_id;

-- name: GetEvent :one
SELECT id, tayfa_id, type, type_version, mode, title, description, starts_at, ends_at,
       meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
       capacity, status, cancelled_at, cancel_reason, series_id, series_override, discoverable,
       created_by, created_at, updated_at, cover_upload_id
FROM events
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Katılım ve düzenleme aynı etkinlik satırını kilitleyerek okur; kapasite
-- ve bekleme sırası iki eşzamanlı istekte birden aşılamaz.
-- name: GetEventForUpdate :one
SELECT id, tayfa_id, type, type_version, mode, title, description, starts_at, ends_at,
       meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
       capacity, status, cancelled_at, cancel_reason, series_id, series_override, discoverable,
       created_by, created_at, updated_at, cover_upload_id
FROM events
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL
FOR UPDATE;

-- Seri örneği elle düzenlenince seri güncellemeleri ona dokunmaz.
-- name: UpdateEvent :execrows
UPDATE events
SET title = @title, description = @description, starts_at = @starts_at, ends_at = @ends_at,
    mode = @mode, meeting_point_name = @meeting_point_name, meeting_lat = @meeting_lat,
    meeting_lng = @meeting_lng, city = @city, district = @district, online_url = @online_url,
    capacity = @capacity, discoverable = @discoverable,
    series_override = (series_id IS NOT NULL)
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL AND status = 'scheduled';

-- name: CancelEvent :execrows
UPDATE events
SET status = 'cancelled', cancelled_at = @cancelled_at, cancelled_by = @cancelled_by,
    cancel_reason = @cancel_reason
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL AND status = 'scheduled';

-- Bitmemiş etkinlikler, başlangıç zamanına göre; (starts_at, id) ile
-- sayfalanır.
-- name: ListUpcomingEvents :many
SELECT id, tayfa_id, type, type_version, mode, title, description, starts_at, ends_at,
       meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
       capacity, status, cancelled_at, cancel_reason, series_id, series_override, discoverable,
       created_by, created_at, updated_at, cover_upload_id
FROM events
WHERE tayfa_id = @tayfa_id
  AND deleted_at IS NULL
  AND ends_at > @now
  AND (starts_at, id) > (@after_starts_at::timestamptz, @after_id::uuid)
ORDER BY starts_at, id
LIMIT @page_size;

-- ---------------------------------------------------------------------------
-- Katılım

-- name: CountRsvpsForEvents :many
SELECT event_id, status, count(*) AS n FROM event_rsvps
WHERE event_id = ANY(@event_ids::uuid[]) AND status IN ('going', 'maybe', 'waitlist')
GROUP BY event_id, status;

-- name: ListUserRsvpsForEvents :many
SELECT event_id, status, attendance FROM event_rsvps
WHERE user_id = @user_id AND event_id = ANY(@event_ids::uuid[]);

-- name: GetRsvp :one
SELECT status, updated_at FROM event_rsvps
WHERE event_id = @event_id AND user_id = @user_id;

-- name: SetRsvp :exec
INSERT INTO event_rsvps (event_id, user_id, status)
VALUES (@event_id, @user_id, @status)
ON CONFLICT (event_id, user_id) DO UPDATE SET status = EXCLUDED.status;

-- name: CountGoing :one
SELECT count(*) FROM event_rsvps
WHERE event_id = @event_id AND status = 'going';

-- Bekleme listesinin başı: en eski kayıt önce. Çağıran etkinlik satırını
-- zaten kilitlemiştir; satır kilidi aynı kaydın iki kez terfisini de önler.
-- name: NextWaitlisted :many
SELECT user_id FROM event_rsvps
WHERE event_id = @event_id AND status = 'waitlist'
ORDER BY updated_at, user_id
LIMIT @n
FOR UPDATE;

-- name: PromoteRsvp :execrows
UPDATE event_rsvps SET status = 'going'
WHERE event_id = @event_id AND user_id = @user_id AND status = 'waitlist';

-- (updated_at, user_id) ile sayfalanır; bekleme listesinde bu sıra
-- terfi sırasıdır.
-- name: ListAttendees :many
SELECT user_id, status, attendance, attendance_method, updated_at FROM event_rsvps
WHERE event_id = @event_id
  AND status = @status
  AND (updated_at, user_id) > (@after_time::timestamptz, @after_user_id::uuid)
ORDER BY updated_at, user_id
LIMIT @page_size;

-- ---------------------------------------------------------------------------
-- Türe özel alan değerleri (issue #237, ADR-0025 §3). Değerler app
-- katmanında etkinliğin tür sürümüne göre doğrulanmış halleriyle yazılır;
-- trigger alanın o tür sürümünde olduğunu ayrıca denetler.

-- name: ListEventFieldValues :many
SELECT field_key, value FROM event_field_values
WHERE event_id = @event_id
ORDER BY field_key;

-- name: SetEventFieldValue :exec
INSERT INTO event_field_values (event_id, field_key, value) VALUES (@event_id, @field_key, @value)
ON CONFLICT (event_id, field_key) DO UPDATE SET value = EXCLUDED.value;

-- name: DeleteEventFieldValues :exec
DELETE FROM event_field_values WHERE event_id = @event_id;

-- Seri örneği şablonun alan değerlerini alır.
-- name: CopyEventFieldValues :exec
INSERT INTO event_field_values (event_id, field_key, value)
SELECT @to_event_id::uuid, src.field_key, src.value FROM event_field_values AS src
WHERE src.event_id = @from_event_id;
