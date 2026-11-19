-- Rota bileşeni (`route`; issue #239; ADR-0025 §4). Etkinlik her zaman önce
-- tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde çözülür
-- (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/route).

-- name: GetEventRoute :one
SELECT points, updated_at FROM event_routes
WHERE event_id = @event_id AND component_key = @component_key;

-- Rota app katmanında doğrulanmış haliyle yazılır; event_routes_points
-- CHECK'i ve event_component_guard aynı kuralları ayrıca denetler.
-- name: SetEventRoute :exec
INSERT INTO event_routes (event_id, component_key, points, created_at, updated_at)
VALUES (@event_id, @component_key, @points, @updated_at, @updated_at)
ON CONFLICT (event_id, component_key) DO UPDATE SET points = EXCLUDED.points, updated_at = EXCLUDED.updated_at;

-- name: DeleteEventRoute :execrows
DELETE FROM event_routes WHERE event_id = @event_id AND component_key = @component_key;

-- Seri örneği şablonun rotalarını alır (bileşen seçimlerinden sonra:
-- tetikleyici bileşenin açık olduğunu denetler).
-- name: CopyEventRoutes :exec
INSERT INTO event_routes (event_id, component_key, points)
SELECT @to_event_id::uuid, src.component_key, src.points FROM event_routes AS src
WHERE src.event_id = @from_event_id;
