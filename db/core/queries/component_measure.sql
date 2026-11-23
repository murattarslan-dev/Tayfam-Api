-- Ölçüm bileşeni (`measure`; issue #245; ADR-0025 §4, §5; ADR-0037 §2, §3).
-- Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür
-- sürümünde çözülür (internal/core/app/component: View / Change); buradaki
-- sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki,
-- sınırlar ve kimin değerini kimin gördüğü app katmanındadır
-- (internal/core/app/component/measure). Değerler float8 olarak okunur ve
-- yazılır; kolon numeric(13, 3)'tür.

-- name: GetEventMeasurementTotal :one
SELECT value::float8 AS value, updated_at FROM event_measurements
WHERE event_id = @event_id AND component_key = @component_key AND user_id IS NULL;

-- (updated_at, id) sırasıyla; bir etkinliğin kişisel değerleri katılımcı
-- sayısıyla sınırlıdır.
-- name: ListEventMeasurements :many
SELECT user_id, value::float8 AS value, updated_at FROM event_measurements
WHERE event_id = @event_id AND component_key = @component_key AND user_id IS NOT NULL
ORDER BY updated_at, id;

-- name: GetEventMeasurement :one
SELECT value::float8 AS value, updated_at FROM event_measurements
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- name: SetEventMeasurementTotal :exec
INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by, created_at, updated_at)
VALUES (@id, @event_id, @component_key, NULL, @value::float8, @entered_by, @now, @now)
ON CONFLICT (event_id, component_key) WHERE user_id IS NULL
DO UPDATE SET value = EXCLUDED.value, entered_by = EXCLUDED.entered_by, updated_at = EXCLUDED.updated_at;

-- name: SetEventMeasurement :exec
INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @user_id, @value::float8, @entered_by, @now, @now)
ON CONFLICT (event_id, component_key, user_id) WHERE user_id IS NOT NULL
DO UPDATE SET value = EXCLUDED.value, entered_by = EXCLUDED.entered_by, updated_at = EXCLUDED.updated_at;

-- name: DeleteEventMeasurementTotal :execrows
DELETE FROM event_measurements
WHERE event_id = @event_id AND component_key = @component_key AND user_id IS NULL;

-- name: DeleteEventMeasurement :execrows
DELETE FROM event_measurements
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- Önerilen değerin kaynağı (`prefill_from`): etkinliğin bir alanının değeri.
-- name: GetMeasurementPrefill :one
SELECT value FROM event_field_values WHERE event_id = @event_id AND field_key = @field_key;
