-- Bileşen çerçevesi (issue #238; ADR-0025 §4–§6, ADR-0037 §2): etkinliğin
-- bileşen seçimleri ve hakemleri. Etkinlik her zaman önce tayfa kapsamında
-- yüklenir (GetEvent / GetEventForUpdate); buradaki sorgular o etkinliğin
-- kimliğiyle çalışır (IDOR). Yetki app katmanındadır
-- (internal/core/app/component).

-- name: ListEventComponents :many
SELECT component_key, enabled, params FROM event_components
WHERE event_id = @event_id
ORDER BY component_key;

-- name: GetEventComponent :one
SELECT enabled, params FROM event_components
WHERE event_id = @event_id AND component_key = @component_key;

-- Seçim app katmanında tür sürümüne göre doğrulanmış haliyle yazılır;
-- trigger (event_components_guard) aynı kuralı ayrıca denetler.
-- name: SetEventComponent :exec
INSERT INTO event_components (event_id, component_key, enabled, params)
VALUES (@event_id, @component_key, @enabled, @params)
ON CONFLICT (event_id, component_key) DO UPDATE SET enabled = EXCLUDED.enabled, params = EXCLUDED.params;

-- Seri örneği şablonun seçimlerini alır.
-- name: CopyEventComponents :exec
INSERT INTO event_components (event_id, component_key, enabled, params)
SELECT @to_event_id::uuid, src.component_key, src.enabled, src.params FROM event_components AS src
WHERE src.event_id = @from_event_id;

-- Kişinin etkinlikteki cevabı ve doğrulanmış katılımı (bileşenlerin
-- "katılımcı" ve "check-in'i doğrulanmış katılımcı" kuralları).
-- name: GetEventParticipation :one
SELECT status, attendance FROM event_rsvps
WHERE event_id = @event_id AND user_id = @user_id;

-- ---------------------------------------------------------------------------
-- Hakemler

-- name: ListEventReferees :many
SELECT id, user_id, component_key, scope_id, created_at FROM event_referees
WHERE event_id = @event_id
ORDER BY created_at, id;

-- name: CountEventReferees :one
SELECT count(*) FROM event_referees WHERE event_id = @event_id;

-- Aynı atama varsa hiçbir şey yapmaz; etkilenen satır 0 olur.
-- name: CreateEventReferee :execrows
INSERT INTO event_referees (id, event_id, user_id, component_key, scope_id, created_at)
VALUES (@id, @event_id, @user_id, @component_key, @scope_id, @created_at)
ON CONFLICT DO NOTHING;

-- name: FindEventReferee :one
SELECT id, created_at FROM event_referees
WHERE event_id = @event_id AND user_id = @user_id
  AND component_key IS NOT DISTINCT FROM sqlc.narg(component_key)::text
  AND scope_id IS NOT DISTINCT FROM sqlc.narg(scope_id)::uuid;

-- Kişi bu bileşende (ve verilmişse bu kapsamda) hakem mi: etkinliğin
-- tamamına, bileşenin tamamına ya da tam bu kapsama yapılmış bir atama.
-- Kapsam verilmemişse yalnızca kapsamsız atamalar sayılır.
-- name: IsEventReferee :one
SELECT EXISTS (
  SELECT 1 FROM event_referees
  WHERE event_id = @event_id AND user_id = @user_id
    AND (component_key IS NULL OR component_key = @component_key::text)
    AND (scope_id IS NULL OR scope_id = sqlc.narg(scope_id)::uuid)
);

-- name: DeleteEventReferee :execrows
DELETE FROM event_referees WHERE event_id = @event_id AND id = @id;

-- Bileşen bir kapsamı (masa, maç) silerken o kapsamın hakem atamalarını da
-- siler.
-- name: DeleteEventScopeReferees :exec
DELETE FROM event_referees
WHERE event_id = @event_id AND component_key = @component_key::text AND scope_id = @scope_id::uuid;

-- Hesap silme: kişinin hakem atamaları silinir.
-- name: DeleteUserEventReferees :exec
DELETE FROM event_referees WHERE user_id = @user_id;
