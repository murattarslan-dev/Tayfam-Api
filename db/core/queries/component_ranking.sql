-- Derece / sıralama bileşeni (`ranking`; issue #253; ADR-0025 §4, §5; ADR-0037
-- §3). Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin
-- tür sürümünde çözülür (internal/core/app/component: View / Change); buradaki
-- sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki,
-- sınırlar ve sıralama app katmanındadır (internal/core/app/component/ranking).
-- Ölçümden hesaplanan derece ölçümün sorgularını okur (component_measure.sql).
-- Değerler float8 olarak okunur ve yazılır; kolon numeric(13, 3)'tür.

-- (updated_at, id) sırasıyla; bir etkinliğin dereceleri katılımcı sayısıyla
-- sınırlıdır.
-- name: ListEventRankingEntries :many
SELECT user_id, value::float8 AS value, updated_at FROM event_ranking_entries
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY updated_at, id;

-- name: GetEventRankingEntry :one
SELECT value::float8 AS value, updated_at FROM event_ranking_entries
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- name: SetEventRankingEntry :exec
INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value, entered_by, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @user_id, @value::float8, @entered_by, @now, @now)
ON CONFLICT (event_id, component_key, user_id)
DO UPDATE SET value = EXCLUDED.value, entered_by = EXCLUDED.entered_by, updated_at = EXCLUDED.updated_at;

-- name: DeleteEventRankingEntry :execrows
DELETE FROM event_ranking_entries
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;
