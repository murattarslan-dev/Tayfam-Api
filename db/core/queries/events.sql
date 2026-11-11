-- Etkinlikler (issue #49). Her sorgu tayfa_id ile ya da tayfa kapsamında
-- yüklenmiş bir etkinliğin kimliğiyle çalışır (IDOR); yetki kontrolü app
-- katmanındadır (authz).

-- name: CreateEvent :exec
INSERT INTO events (
  id, tayfa_id, type, mode, title, description, starts_at, ends_at,
  meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
  capacity, discoverable, created_by, client_request_id
) VALUES (
  @id, @tayfa_id, @type, @mode, @title, @description, @starts_at, @ends_at,
  @meeting_point_name, @meeting_lat, @meeting_lng, @city, @district, @online_url,
  @capacity, @discoverable, @created_by, @client_request_id
);

-- Tekrar gönderilen oluşturma isteği aynı etkinliği döndürür.
-- name: GetEventByClientRequest :one
SELECT id, tayfa_id, deleted_at FROM events
WHERE created_by = @created_by AND client_request_id = @client_request_id;

-- name: GetEvent :one
SELECT id, tayfa_id, type, mode, title, description, starts_at, ends_at,
       meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
       capacity, status, cancelled_at, cancel_reason, series_id, series_override, discoverable,
       created_by, created_at, updated_at, cover_upload_id
FROM events
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Katılım ve düzenleme aynı etkinlik satırını kilitleyerek okur; kapasite
-- ve bekleme sırası iki eşzamanlı istekte birden aşılamaz.
-- name: GetEventForUpdate :one
SELECT id, tayfa_id, type, mode, title, description, starts_at, ends_at,
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
SELECT id, tayfa_id, type, mode, title, description, starts_at, ends_at,
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
-- Türe özel detaylar. Detay satırı türün tablosuna yazılır; bileşik foreign
-- key yanlış türü reddeder.

-- name: SetEventConvoy :exec
INSERT INTO event_convoy (event_id, route) VALUES (@event_id, @route)
ON CONFLICT (event_id) DO UPDATE SET route = EXCLUDED.route;

-- name: GetEventConvoy :one
SELECT route FROM event_convoy WHERE event_id = @event_id;

-- name: SetEventCamp :exec
INSERT INTO event_camp (event_id, site_name, has_water, has_toilet, notes)
VALUES (@event_id, @site_name, @has_water, @has_toilet, @notes)
ON CONFLICT (event_id) DO UPDATE
SET site_name = EXCLUDED.site_name, has_water = EXCLUDED.has_water,
    has_toilet = EXCLUDED.has_toilet, notes = EXCLUDED.notes;

-- name: GetEventCamp :one
SELECT site_name, has_water, has_toilet, notes FROM event_camp WHERE event_id = @event_id;

-- name: SetEventHike :exec
INSERT INTO event_hike (event_id, difficulty, distance_km, elevation_m, expected_return_at, return_grace_minutes)
VALUES (@event_id, @difficulty, sqlc.narg(distance_km)::float8, @elevation_m, @expected_return_at, @return_grace_minutes)
ON CONFLICT (event_id) DO UPDATE
SET difficulty = EXCLUDED.difficulty, distance_km = EXCLUDED.distance_km,
    elevation_m = EXCLUDED.elevation_m, expected_return_at = EXCLUDED.expected_return_at,
    return_grace_minutes = EXCLUDED.return_grace_minutes;

-- name: GetEventHike :one
SELECT difficulty, COALESCE(distance_km, 0)::float8 AS distance_km, elevation_m, expected_return_at, return_grace_minutes
FROM event_hike WHERE event_id = @event_id;

-- name: SetEventFishing :exec
INSERT INTO event_fishing (event_id, spot_visibility) VALUES (@event_id, @spot_visibility)
ON CONFLICT (event_id) DO UPDATE SET spot_visibility = EXCLUDED.spot_visibility;

-- name: GetEventFishing :one
SELECT spot_visibility FROM event_fishing WHERE event_id = @event_id;

-- Liste görünümünde mera gizliliği için.
-- name: ListFishingVisibility :many
SELECT event_id, spot_visibility FROM event_fishing
WHERE event_id = ANY(@event_ids::uuid[]);

-- name: SetEventGaming :exec
INSERT INTO event_gaming (event_id, game, platform) VALUES (@event_id, @game, @platform)
ON CONFLICT (event_id) DO UPDATE SET game = EXCLUDED.game, platform = EXCLUDED.platform;

-- name: GetEventGaming :one
SELECT game, platform FROM event_gaming WHERE event_id = @event_id;

-- name: SetEventSports :exec
INSERT INTO event_sports (event_id, sport, venue_name) VALUES (@event_id, @sport, @venue_name)
ON CONFLICT (event_id) DO UPDATE SET sport = EXCLUDED.sport, venue_name = EXCLUDED.venue_name;

-- name: GetEventSports :one
SELECT sport, venue_name FROM event_sports WHERE event_id = @event_id;

-- ---------------------------------------------------------------------------
-- Kamp malzemeleri

-- name: CountCampItems :one
SELECT count(*) FROM event_camp_items WHERE event_id = @event_id;

-- name: CreateCampItem :exec
INSERT INTO event_camp_items (id, event_id, name, quantity, assigned_user_id, created_by)
VALUES (@id, @event_id, @name, @quantity, @assigned_user_id, @created_by);

-- name: ListCampItems :many
SELECT id, name, quantity, assigned_user_id FROM event_camp_items
WHERE event_id = @event_id
ORDER BY created_at, id;

-- name: DeleteCampItem :execrows
DELETE FROM event_camp_items WHERE event_id = @event_id AND id = @id;

-- ---------------------------------------------------------------------------
-- Konvoy araçları. Kişi başına bir araç; yeniden eklemek öncekinin yerine
-- geçer.

-- name: SetConvoyVehicle :one
INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, model, model_year, color, plate)
VALUES (@id, @event_id, @user_id, @make, @model, @model_year, @color, @plate)
ON CONFLICT (event_id, user_id) DO UPDATE
SET make = EXCLUDED.make, model = EXCLUDED.model, model_year = EXCLUDED.model_year,
    color = EXCLUDED.color, plate = EXCLUDED.plate
RETURNING id;

-- name: ListConvoyVehicles :many
SELECT id, user_id, make, model, model_year, color, plate FROM event_convoy_vehicles
WHERE event_id = @event_id
ORDER BY created_at, id;

-- name: GetConvoyVehicleOwner :one
SELECT user_id FROM event_convoy_vehicles WHERE event_id = @event_id AND id = @id;

-- name: DeleteConvoyVehicle :execrows
DELETE FROM event_convoy_vehicles WHERE event_id = @event_id AND id = @id;

-- "Geliyorum"dan vazgeçen kişinin aracı (ve plakası) kalmaz.
-- name: DeleteUserConvoyVehicle :execrows
DELETE FROM event_convoy_vehicles WHERE event_id = @event_id AND user_id = @user_id;
