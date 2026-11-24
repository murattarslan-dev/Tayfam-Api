-- Tayfa ve kişisel istatistik (issue #266; ADR-0026 §1–§2, ADR-0037 §7).
-- Kaynak yalnızca donmuş etkinliklerdir: bitişinden 7 gün geçmiş
-- (frozen_before = şimdi - component.FreezeAfter), silinmemiş, iptal
-- edilmemiş. Hesaplama app katmanındadır (internal/core/app/stats).

-- Yeni donan (ya da geri gelen) etkinlikleri istatistiğe alınmış işaretler
-- ve tayfalarını döndürür; yeniden hesaplanacaklar kuyruğa girer.
-- name: MarkCountedStatEvents :many
INSERT INTO event_stat_runs (event_id, tayfa_id, counted, changed_at)
SELECT e.id, e.tayfa_id, true, @now::timestamptz
FROM events AS e
WHERE e.ends_at <= @frozen_before::timestamptz AND e.deleted_at IS NULL AND e.status <> 'cancelled'
  AND NOT EXISTS (SELECT 1 FROM event_stat_runs AS r WHERE r.event_id = e.id AND r.counted)
ORDER BY e.ends_at, e.id
LIMIT @n
ON CONFLICT (event_id) DO UPDATE SET counted = true, changed_at = EXCLUDED.changed_at
RETURNING tayfa_id;

-- İstatistikteyken silinen ya da iptal edilen etkinlikleri düşmüş
-- işaretler ve tayfalarını döndürür.
-- name: MarkDroppedStatEvents :many
UPDATE event_stat_runs AS r SET counted = false, changed_at = @now::timestamptz
WHERE r.event_id IN (
  SELECT x.event_id FROM event_stat_runs AS x
  JOIN events AS e ON e.id = x.event_id
  WHERE x.counted AND (e.deleted_at IS NOT NULL OR e.status = 'cancelled')
  ORDER BY x.event_id
  LIMIT @n
)
RETURNING r.tayfa_id;

-- Tayfayı yeniden hesaplanacaklar kuyruğuna ekler; artık olmayan tayfayı
-- sessizce atlar (sözlük olayı tayfa silindikten sonra da gelebilir).
-- name: QueueTayfaStats :exec
INSERT INTO tayfa_stat_queue (tayfa_id, queued_at)
SELECT t.id, @queued_at::timestamptz FROM tayfas AS t WHERE t.id = @tayfa_id
ON CONFLICT (tayfa_id) DO NOTHING;

-- Kuyruktan sıradaki tayfaları kilitleyerek alır; satır hesaplamayla aynı
-- transaction'da silinir. Kilitliyken gelen yeni istek silinmeden sonra
-- yeniden eklenir, böylece hiçbir değişiklik kaybolmaz.
-- name: ClaimTayfaStats :many
SELECT tayfa_id FROM tayfa_stat_queue
ORDER BY queued_at, tayfa_id
LIMIT @n
FOR UPDATE SKIP LOCKED;

-- name: DeleteTayfaStatQueue :exec
DELETE FROM tayfa_stat_queue WHERE tayfa_id = @tayfa_id;

-- Tayfanın donmuş etkinlikleri: tür sürümü, yıl (Europe/Istanbul), süre
-- (dakika) ve katılımın hassas olup olmadığı.
-- name: ListStatEvents :many
SELECT e.id, e.type, e.type_version,
  extract(year FROM e.starts_at AT TIME ZONE 'Europe/Istanbul')::int AS year,
  floor(extract(epoch FROM e.ends_at - e.starts_at) / 60)::bigint AS minutes,
  event_attendance_sensitive(e.id)::boolean AS sensitive
FROM events AS e
WHERE e.tayfa_id = @tayfa_id AND e.ends_at <= @frozen_before::timestamptz
  AND e.deleted_at IS NULL AND e.status <> 'cancelled'
ORDER BY e.id;

-- Organizatörün açıp kapattığı bileşenlerin seçimleri.
-- name: ListStatComponentChoices :many
SELECT c.event_id, c.component_key, c.enabled
FROM event_components AS c
JOIN events AS e ON e.id = c.event_id
WHERE e.tayfa_id = @tayfa_id AND e.ends_at <= @frozen_before::timestamptz
  AND e.deleted_at IS NULL AND e.status <> 'cancelled';

-- Doğrulanmış katılımlar.
-- name: ListStatAttendance :many
SELECT r.event_id, r.user_id
FROM event_rsvps AS r
JOIN events AS e ON e.id = r.event_id
WHERE e.tayfa_id = @tayfa_id AND e.ends_at <= @frozen_before::timestamptz
  AND e.deleted_at IS NULL AND e.status <> 'cancelled'
  AND r.attendance = 'attended'
ORDER BY r.event_id, r.user_id;

-- Kaydedilmiş ölçümler (öneri tabloda yoktur, sayılmaz).
-- name: ListStatMeasurements :many
SELECT m.event_id, m.component_key, m.user_id, m.value::float8 AS value
FROM event_measurements AS m
JOIN events AS e ON e.id = m.event_id
WHERE e.tayfa_id = @tayfa_id AND e.ends_at <= @frozen_before::timestamptz
  AND e.deleted_at IS NULL AND e.status <> 'cancelled'
ORDER BY m.event_id, m.component_key, m.user_id;

-- Silinmemiş günlük kayıtları; sözlüğe bağlı alanlar birleştirme hedefine
-- çözülmüş değer kimliğiyle (alan → kimlik). Konum okunmaz.
-- name: ListStatLogEntries :many
SELECT l.event_id, l.component_key, l.user_id, l.field_values,
  COALESCE((
    SELECT jsonb_object_agg(x.field_key, COALESCE(v.merged_into, v.id))
    FROM event_log_entry_vocabulary AS x
    JOIN tayfa_vocabulary AS v ON v.id = x.vocabulary_id
    WHERE x.entry_id = l.id
  ), '{}')::jsonb AS vocabulary
FROM event_log_entries AS l
JOIN events AS e ON e.id = l.event_id
WHERE e.tayfa_id = @tayfa_id AND e.ends_at <= @frozen_before::timestamptz
  AND e.deleted_at IS NULL AND e.status <> 'cancelled'
  AND l.deleted_at IS NULL
ORDER BY l.event_id, l.id;

-- name: DeleteTayfaStats :exec
DELETE FROM tayfa_stats WHERE tayfa_id = @tayfa_id;

-- name: DeleteTayfaUserStats :exec
DELETE FROM user_stats WHERE tayfa_id = @tayfa_id;

-- name: InsertTayfaStats :exec
INSERT INTO tayfa_stats (tayfa_id, year, metric_key, source, value, event_count, computed_at)
SELECT @tayfa_id::uuid, unnest(@years::int[]), unnest(@metric_keys::text[]), unnest(@sources::text[]),
  unnest(@vals::float8[]), unnest(@event_counts::int[]), @computed_at::timestamptz;

-- name: InsertUserStats :exec
INSERT INTO user_stats (user_id, tayfa_id, year, metric_key, source, value, event_count, computed_at)
SELECT unnest(@user_ids::uuid[]), @tayfa_id::uuid, unnest(@years::int[]), unnest(@metric_keys::text[]),
  unnest(@sources::text[]), unnest(@vals::float8[]), unnest(@event_counts::int[]), @computed_at::timestamptz;

-- Kişinin bütün tayfalardaki toplamları, yıl ve metrik başına (rozetler).
-- name: ListUserStatSums :many
SELECT year, metric_key, sum(value)::float8 AS value
FROM user_stats
WHERE user_id = @user_id
GROUP BY year, metric_key
ORDER BY year, metric_key;

-- Okuma (tayfa içi uç; #267 vitrin ve profil, #268 ana sayfa kartları).

-- name: ListTayfaStatYears :many
SELECT DISTINCT year FROM tayfa_stats
WHERE tayfa_id = @tayfa_id AND year > 0
ORDER BY year DESC;

-- name: ListTayfaStats :many
SELECT metric_key, source, value::float8 AS value, event_count, computed_at
FROM tayfa_stats
WHERE tayfa_id = @tayfa_id AND year = @year
ORDER BY metric_key;

-- Çağıranın kendi katkıları: ölçüm ve günlük metrikleri, yalnızca kişinin
-- tayfanın etkin üyesiyken. Başkasının katkısı hiçbir zaman okunmaz
-- (platform sahibi, 2026-11-23; issue #322, ADR-0026 §3).
-- name: ListOwnTayfaStatContributions :many
SELECT s.metric_key, s.source, s.value::float8 AS value, s.event_count
FROM user_stats AS s
JOIN memberships AS m ON m.tayfa_id = s.tayfa_id AND m.user_id = s.user_id AND m.status = 'active'
WHERE s.tayfa_id = @tayfa_id AND s.user_id = @user_id AND s.year = @year AND s.source IN ('measure', 'log')
ORDER BY s.metric_key;

-- Kişinin bütün tayfalardaki toplamları bir yıl (0: tüm zamanlar) için.
-- name: ListUserStats :many
SELECT metric_key, source, sum(value)::float8 AS value, sum(event_count)::int AS event_count
FROM user_stats
WHERE user_id = @user_id AND year = @year
GROUP BY metric_key, source
ORDER BY metric_key;
