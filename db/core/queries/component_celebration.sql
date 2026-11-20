-- Kutlama bileşeni (`celebration`; issue #255; ADR-0025 §4, §5). Etkinlik her
-- zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde
-- çözülür (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki,
-- kaynaktan kazanan okuma ve bildirim app katmanındadır
-- (internal/core/app/component/celebration).

-- name: GetEventCelebration :one
SELECT announced_at, updated_at FROM event_celebrations
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CreateEventCelebration :exec
INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at)
VALUES (@event_id, @component_key, @announced_by, @now, @now);

-- name: TouchEventCelebration :exec
UPDATE event_celebrations SET updated_at = @now
WHERE event_id = @event_id AND component_key = @component_key;

-- Elle seçilen kazananlar, organizatörün verdiği sırayla; en fazla on kişi.
-- name: ListEventCelebrationWinners :many
SELECT user_id FROM event_celebration_winners
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY position;

-- name: AddEventCelebrationWinner :exec
INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position, created_at)
VALUES (@id, @event_id, @component_key, @user_id, @position, @now);

-- name: DeleteEventCelebrationWinners :exec
DELETE FROM event_celebration_winners
WHERE event_id = @event_id AND component_key = @component_key;
