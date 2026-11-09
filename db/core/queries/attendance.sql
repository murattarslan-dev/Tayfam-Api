-- Katılım doğrulama, organizatör düzeltmesi, yürüyüş yoklaması, kalıcı
-- katılım geçmişi ve av günlüğü (issue #50). Etkinlik her zaman önce tayfa
-- kapsamında yüklenip kilitlenir (GetEventForUpdate); buradaki sorgular o
-- etkinliğin kimliğiyle çalışır. Check-in'de gönderilen konum hiçbir
-- sorguya parametre olarak girmez.

-- name: GetRsvpForCheckIn :one
SELECT status, status_changed_at, attendance, attendance_method, check_in_attempts
FROM event_rsvps
WHERE event_id = @event_id AND user_id = @user_id
FOR UPDATE;

-- Deneme yalnızca sınırın altındayken sayılır; 0 satır: sınır dolmuş.
-- name: CountCheckInAttempt :execrows
UPDATE event_rsvps SET check_in_attempts = check_in_attempts + 1
WHERE event_id = @event_id AND user_id = @user_id AND check_in_attempts < 5;

-- name: MarkAttended :exec
UPDATE event_rsvps
SET attendance = 'attended', attendance_method = @method, checked_in_at = @checked_in_at,
    attendance_marked_by = @marked_by, attendance_marked_at = @marked_at
WHERE event_id = @event_id AND user_id = @user_id;

-- Organizatörün eklediği kişinin yanıtı yoksa "geliyorum" olarak açılır;
-- etkinlik bitmiş olduğu için kapasiteyi etkilemez.
-- name: InsertAttendedRsvp :exec
INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, attendance_marked_by, attendance_marked_at)
VALUES (@event_id, @user_id, 'going', 'attended', 'organizer', @marked_by, @marked_at);

-- name: MarkNoShow :exec
UPDATE event_rsvps
SET attendance = 'no_show', attendance_method = 'organizer', checked_in_at = NULL,
    attendance_marked_by = @marked_by, attendance_marked_at = @marked_at
WHERE event_id = @event_id AND user_id = @user_id;

-- name: IsActiveMember :one
SELECT EXISTS (
  SELECT 1 FROM memberships
  WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'active'
);

-- ---------------------------------------------------------------------------
-- Yürüyüş yoklaması

-- name: GetHikeCheckin :one
SELECT started_at, started_received_at, returned_at, returned_received_at
FROM event_hike_checkins
WHERE event_id = @event_id AND user_id = @user_id;

-- name: StartHikeCheckin :execrows
INSERT INTO event_hike_checkins (event_id, user_id, started_at, started_received_at)
VALUES (@event_id, @user_id, @started_at, @received_at)
ON CONFLICT (event_id, user_id) DO NOTHING;

-- name: ReturnHikeCheckin :execrows
UPDATE event_hike_checkins
SET returned_at = @returned_at, returned_received_at = @received_at
WHERE event_id = @event_id AND user_id = @user_id AND returned_at IS NULL;

-- ---------------------------------------------------------------------------
-- Kalıcı katılım geçmişi

-- name: UpsertActivityRecord :exec
INSERT INTO user_activity_records (
  id, user_id, event_id, tayfa_id, event_type, mode, event_title, city, district,
  started_at, duration_minutes, attendance_method
) VALUES (
  @id, @user_id, @event_id, @tayfa_id, @event_type, @mode, @event_title, @city, @district,
  @started_at, @duration_minutes, @attendance_method
)
ON CONFLICT (user_id, event_id) DO UPDATE
SET event_type = EXCLUDED.event_type, mode = EXCLUDED.mode, event_title = EXCLUDED.event_title,
    city = EXCLUDED.city, district = EXCLUDED.district, started_at = EXCLUDED.started_at,
    duration_minutes = EXCLUDED.duration_minutes, attendance_method = EXCLUDED.attendance_method;

-- name: DeleteActivityRecord :execrows
DELETE FROM user_activity_records WHERE user_id = @user_id AND event_id = @event_id;

-- Etkinlik düzenlenince anlık görüntüsü yenilenecek kayıtlar.
-- name: ListActivityRecordsOfEvent :many
SELECT user_id, attendance_method FROM user_activity_records
WHERE event_id = @event_id
ORDER BY user_id;

-- ---------------------------------------------------------------------------
-- Av günlüğü

-- name: GetCatchByClientRequest :one
SELECT id, event_id, deleted_at FROM fishing_catches
WHERE user_id = @user_id AND client_request_id = @client_request_id;

-- name: CountUserCatches :one
SELECT count(*) FROM fishing_catches
WHERE event_id = @event_id AND user_id = @user_id AND deleted_at IS NULL;

-- name: CreateCatch :exec
INSERT INTO fishing_catches (id, event_id, user_id, species, length_cm, lat, lng, visibility, client_request_id)
VALUES (@id, @event_id, @user_id, @species, sqlc.arg(length_cm)::float8, @lat, @lng, @visibility, @client_request_id);

-- name: GetCatch :one
SELECT id, user_id, species, length_cm::float8 AS length_cm, lat, lng, visibility, created_at
FROM fishing_catches
WHERE event_id = @event_id AND id = @id AND deleted_at IS NULL;

-- (created_at, id) ile sayfalanır.
-- name: ListCatches :many
SELECT id, user_id, species, length_cm::float8 AS length_cm, lat, lng, visibility, created_at
FROM fishing_catches
WHERE event_id = @event_id
  AND deleted_at IS NULL
  AND (created_at, id) > (@after_time::timestamptz, @after_id::uuid)
ORDER BY created_at, id
LIMIT @page_size;

-- name: DeleteCatch :execrows
UPDATE fishing_catches SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE event_id = @event_id AND id = @id AND deleted_at IS NULL;
