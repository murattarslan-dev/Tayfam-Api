-- Kura bileşeni (`draw`; issue #249; ADR-0025 §4). Etkinlik her zaman önce
-- tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde çözülür
-- (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/draw).

-- name: GetDraw :one
SELECT subject, team_key, drawn_at FROM event_draws
WHERE event_id = @event_id AND component_key = @component_key;

-- Kuranın sırası. active_member, kişinin hâlâ tayfanın etkin üyesi olup
-- olmadığıdır: tayfadan ayrılanın yeri boş gösterilir.
-- name: ListDrawEntries :many
SELECT d.position, d.user_id, d.team_id, d.guest_id, (ms.user_id IS NOT NULL)::boolean AS active_member
FROM event_draw_entries AS d
JOIN events AS e ON e.id = d.event_id
LEFT JOIN memberships AS ms ON ms.tayfa_id = e.tayfa_id AND ms.user_id = d.user_id AND ms.status = 'active'
WHERE d.event_id = @event_id AND d.component_key = @component_key
ORDER BY d.position;

-- Kuraya girecek kişiler: tayfanın etkin üyesi olan ve "geliyorum" demiş
-- ya da katılımı doğrulanmış katılımcılar, kimlik sırasıyla.
-- name: ListDrawCandidates :many
SELECT r.user_id FROM event_rsvps AS r
JOIN events AS e ON e.id = r.event_id
JOIN memberships AS ms ON ms.tayfa_id = e.tayfa_id AND ms.user_id = r.user_id AND ms.status = 'active'
WHERE r.event_id = @event_id AND (r.status = 'going' OR r.attendance = 'attended')
ORDER BY r.user_id;

-- name: CreateDraw :exec
INSERT INTO event_draws (event_id, component_key, subject, team_key, drawn_at)
VALUES (@event_id, @component_key, @subject, @team_key, @drawn_at);

-- name: AddDrawEntry :exec
INSERT INTO event_draw_entries (id, event_id, component_key, position, user_id, team_key, team_id, guest_id, created_at)
VALUES (@id, @event_id, @component_key, @position, @user_id, @team_key, @team_id, @guest_id, @created_at);
