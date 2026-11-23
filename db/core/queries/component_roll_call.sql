-- Yoklama ve dönmeme alarmı bileşeni (`roll_call`; issue #242; ADR-0025 §4).
-- Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür
-- sürümünde çözülür (internal/core/app/component: View / Change); buradaki
-- sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR).
-- Yetki app katmanındadır (internal/core/app/component/rollcall). Dönmeme
-- alarmı işi jobs.sql'dedir (LockOverdueHikers).

-- name: GetRollCallPlan :one
SELECT expected_return_at, updated_at FROM event_roll_calls
WHERE event_id = @event_id AND component_key = @component_key;

-- name: SetRollCallPlan :exec
INSERT INTO event_roll_calls (event_id, component_key, expected_return_at, created_at, updated_at)
VALUES (@event_id, @component_key, @expected_return_at, @updated_at, @updated_at)
ON CONFLICT (event_id, component_key) DO UPDATE
SET expected_return_at = EXCLUDED.expected_return_at, updated_at = EXCLUDED.updated_at;

-- name: DeleteRollCallPlan :execrows
DELETE FROM event_roll_calls WHERE event_id = @event_id AND component_key = @component_key;

-- Seri örneği şablonun planını alır; beklenen dönüş örneğin başlangıcına
-- göre kaydırılır (Faz 1 ile aynı). Bileşen seçimlerinden sonra çalışır:
-- tetikleyici bileşenin açık olduğunu denetler.
-- name: CopyRollCallPlans :exec
INSERT INTO event_roll_calls (event_id, component_key, expected_return_at)
SELECT dst.id, src.component_key, src.expected_return_at + (dst.starts_at - tmpl.starts_at)
FROM event_roll_calls AS src
JOIN events AS tmpl ON tmpl.id = src.event_id
JOIN events AS dst ON dst.id = @to_event_id
WHERE src.event_id = @from_event_id;

-- name: GetRollCallCheckin :one
SELECT started_at, returned_at FROM event_roll_call_checkins
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- Tekrar gönderim ilk çıkışı korur (0 satır).
-- name: StartRollCallCheckin :execrows
INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at)
VALUES (@event_id, @component_key, @user_id, @started_at, @received_at)
ON CONFLICT (event_id, component_key, user_id) DO NOTHING;

-- Tekrar gönderim ilk dönüşü korur (0 satır).
-- name: ReturnRollCallCheckin :execrows
UPDATE event_roll_call_checkins
SET returned_at = @returned_at, returned_received_at = @received_at
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id AND returned_at IS NULL;

-- Yönetenlerin gördüğü liste: önce dışarıdakiler, sonra çıkış sırası.
-- name: ListRollCallCheckins :many
SELECT user_id, started_at, returned_at FROM event_roll_call_checkins
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY returned_at IS NOT NULL, started_at, user_id
LIMIT @n;
