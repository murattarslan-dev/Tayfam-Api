-- Tekrarlayan etkinlik serileri (issue #51). Kullanıcı istekleri her sorguyu
-- tayfa_id ile çalıştırır (IDOR); üretim işi seriyi kendi listesinden alır.

-- name: CreateEventSeries :exec
INSERT INTO event_series (id, tayfa_id, template_event_id, rrule, timezone, dtstart, created_by)
VALUES (@id, @tayfa_id, @template_event_id, @rrule, @timezone, @dtstart, @created_by);

-- name: SetEventSeriesID :execrows
UPDATE events SET series_id = @series_id
WHERE tayfa_id = @tayfa_id AND id = @id AND series_id IS NULL;

-- name: GetEventSeries :one
SELECT id, tayfa_id, template_event_id, rrule, timezone, dtstart, generated_until,
       ended_at, created_by, created_at
FROM event_series
WHERE tayfa_id = @tayfa_id AND id = @id;

-- Seriyi değiştiren istekler ve üretim işi aynı satırı kilitler; çağıran
-- tayfa satırını önceden kilitlemiştir (kilit sırası: tayfa, seri,
-- etkinlikler).
-- name: GetEventSeriesForUpdate :one
SELECT id, tayfa_id, template_event_id, rrule, timezone, dtstart, generated_until,
       ended_at, created_by, created_at
FROM event_series
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- Üretim sırası gelen seriler: bitmemiş, şablonu olan ve generated_until'ı
-- verilen zamandan önce kalanlar.
-- name: ListDueEventSeries :many
SELECT id, tayfa_id FROM event_series
WHERE ended_at IS NULL
  AND template_event_id IS NOT NULL
  AND (generated_until IS NULL OR generated_until < @due_before)
ORDER BY generated_until NULLS FIRST, id
LIMIT @n;

-- generated_until yalnızca ileri gider.
-- name: AdvanceEventSeries :exec
UPDATE event_series SET generated_until = @generated_until
WHERE id = @id AND (generated_until IS NULL OR generated_until < @generated_until);

-- name: SetEventSeriesTemplate :exec
UPDATE event_series SET template_event_id = @template_event_id
WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: EndEventSeries :execrows
UPDATE event_series SET ended_at = @ended_at
WHERE tayfa_id = @tayfa_id AND id = @id AND ended_at IS NULL;

-- Üretilen örnek. Aynı seride aynı başlangıç zamanı zaten varsa hiçbir şey
-- yazılmaz (events_series_starts_at_key); iki pod aynı örneği üretemez.
-- name: CreateSeriesEvent :execrows
INSERT INTO events (
  id, tayfa_id, type, mode, title, description, starts_at, ends_at,
  meeting_point_name, meeting_lat, meeting_lng, city, district, online_url,
  capacity, discoverable, created_by, series_id
) VALUES (
  @id, @tayfa_id, @type, @mode, @title, @description, @starts_at, @ends_at,
  @meeting_point_name, @meeting_lat, @meeting_lng, @city, @district, @online_url,
  @capacity, @discoverable, @created_by, @series_id
)
ON CONFLICT (series_id, starts_at) WHERE series_id IS NOT NULL DO NOTHING;

-- Serinin henüz başlamamış, iptal edilmemiş ve tek başına düzenlenmemiş
-- örnekleri; seri güncellemesi bunları değiştirir.
-- name: ListSeriesUpcomingEvents :many
SELECT id FROM events
WHERE tayfa_id = @tayfa_id AND series_id = @series_id AND deleted_at IS NULL
  AND status = 'scheduled' AND NOT series_override AND starts_at > @now
ORDER BY starts_at, id
FOR UPDATE;

-- Seri güncellemesi: UpdateEvent gibi, ama series_override'a dokunmaz.
-- name: UpdateSeriesEvent :execrows
UPDATE events
SET title = @title, description = @description, mode = @mode,
    meeting_point_name = @meeting_point_name, meeting_lat = @meeting_lat,
    meeting_lng = @meeting_lng, city = @city, district = @district, online_url = @online_url,
    capacity = @capacity, discoverable = @discoverable
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Seri sonlandırılınca gelecekteki düzenlenmemiş örnekler iptal edilir.
-- name: CancelSeriesUpcomingEvents :many
UPDATE events
SET status = 'cancelled', cancelled_at = @now, cancelled_by = @cancelled_by
WHERE tayfa_id = @tayfa_id AND series_id = @series_id AND deleted_at IS NULL
  AND status = 'scheduled' AND NOT series_override AND starts_at > @now
RETURNING id;
