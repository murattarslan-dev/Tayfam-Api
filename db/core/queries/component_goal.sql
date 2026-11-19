-- Hedef çubuğu bileşeni (`goal`; issue #246; ADR-0025 §4, §5). Etkinlik her
-- zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde
-- çözülür (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki, bağlı
-- ölçüm ve ilerlemeyi kimin gördüğü app katmanındadır
-- (internal/core/app/component/goal). Sayılar float8 olarak okunur ve
-- yazılır; kolonlar numeric(13, 3)'tür.

-- name: GetEventGoal :one
SELECT target::float8 AS target, updated_at FROM event_goals
WHERE event_id = @event_id AND component_key = @component_key;

-- name: SetEventGoal :exec
INSERT INTO event_goals (event_id, component_key, target, created_at, updated_at)
VALUES (@event_id, @component_key, @target::float8, @now, @now)
ON CONFLICT (event_id, component_key)
DO UPDATE SET target = EXCLUDED.target, updated_at = EXCLUDED.updated_at;

-- name: DeleteEventGoal :execrows
DELETE FROM event_goals WHERE event_id = @event_id AND component_key = @component_key;

-- İlerleme: bağlı ölçümün kaydedilmiş değerleri (öneri tabloya yazılmaz).
-- Toplam ölçümde tek satır (user_id NULL), kişisel ölçümde kişi başına satır;
-- personal ölçümün kapsamına göre biri seçilir. Değer yoksa 0, count 0.
-- name: SumEventGoalProgress :one
SELECT COALESCE(sum(value), 0)::float8 AS total, count(*)::bigint AS entries
FROM event_measurements
WHERE event_id = @event_id AND component_key = @measure_key
  AND (user_id IS NOT NULL) = @personal::boolean;

-- Seri örneği şablonun hedefini alır (planın parçası); satır yoksa örnek de
-- tanımdaki varsayılanla başlar.
-- name: CopyEventGoals :exec
INSERT INTO event_goals (event_id, component_key, target)
SELECT @to_event_id::uuid, src.component_key, src.target FROM event_goals AS src
WHERE src.event_id = @from_event_id;
