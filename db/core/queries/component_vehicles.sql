-- Araçlar ve koltuk paylaşımı bileşeni (`vehicles`; issue #240; ADR-0025 §4,
-- ADR-0037 §3). Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen
-- etkinliğin tür sürümünde çözülür (internal/core/app/component: View /
-- Change); buradaki sorgular o etkinliğin kimliği ve bileşenin anahtarıyla
-- çalışır (IDOR). Yetki app katmanındadır
-- (internal/core/app/component/vehicles).

-- name: ListEventVehicles :many
SELECT id, user_id, make, model, model_year, color, plate, seats FROM event_vehicles
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY created_at, id;

-- name: GetEventVehicle :one
SELECT user_id, seats FROM event_vehicles
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- name: GetUserEventVehicle :one
SELECT id FROM event_vehicles
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- Kişi başına bir araç; yeniden yazmak öncekinin yerine geçer (kimliği
-- değişmez). Değerler app katmanında doğrulanmış haliyle yazılır.
-- name: SetEventVehicle :one
INSERT INTO event_vehicles (id, event_id, component_key, user_id, make, model, model_year, color, plate, seats, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @user_id, @make, @model, @model_year, @color, @plate, @seats, @updated_at, @updated_at)
ON CONFLICT (event_id, component_key, user_id) DO UPDATE
SET make = EXCLUDED.make, model = EXCLUDED.model, model_year = EXCLUDED.model_year,
    color = EXCLUDED.color, plate = EXCLUDED.plate, seats = EXCLUDED.seats, updated_at = EXCLUDED.updated_at
RETURNING id;

-- Aracın koltuk istekleri foreign key ile birlikte gider.
-- name: DeleteEventVehicle :execrows
DELETE FROM event_vehicles
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- name: ListEventSeatRequests :many
SELECT event_vehicle_id, user_id, status FROM event_vehicle_seat_requests
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY created_at, id;

-- name: GetEventSeatRequest :one
SELECT event_vehicle_id, status FROM event_vehicle_seat_requests
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- name: CountApprovedSeats :one
SELECT count(*) FROM event_vehicle_seat_requests
WHERE event_id = @event_id AND component_key = @component_key AND event_vehicle_id = @event_vehicle_id AND status = 'approved';

-- name: CreateEventSeatRequest :exec
INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id, created_at)
VALUES (@id, @event_id, @component_key, @user_id, @event_vehicle_id, @created_at);

-- name: ApproveEventSeatRequest :execrows
UPDATE event_vehicle_seat_requests SET status = 'approved', decided_at = @decided_at
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id
  AND event_vehicle_id = @event_vehicle_id AND status = 'pending';

-- name: DeleteEventSeatRequest :execrows
DELETE FROM event_vehicle_seat_requests
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- "Geliyorum"dan vazgeçen kişinin aracı (plakası ve aracına gelen istekler)
-- ve kendi koltuk isteği kalmaz; etkinliğin bütün araçlar bileşenlerinde.
-- name: DeleteUserEventVehicles :execrows
DELETE FROM event_vehicles WHERE event_id = @event_id AND user_id = @user_id;

-- name: DeleteUserEventSeatRequests :execrows
DELETE FROM event_vehicle_seat_requests WHERE event_id = @event_id AND user_id = @user_id;
