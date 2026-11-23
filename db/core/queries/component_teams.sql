-- Takım kurma bileşeni (`teams`; issue #248; ADR-0025 §4). Etkinlik her
-- zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde
-- çözülür (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/teams).

-- name: ListTeams :many
SELECT id, position, name FROM event_teams
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY position;

-- Takımlardaki kişiler; yalnızca hâlâ katılımcı olanlar (tayfanın etkin
-- üyesi ve "geliyorum" demiş ya da katılımı doğrulanmış). Cevabını
-- değiştiren ya da tayfadan ayrılan kişi takımda görünmez.
-- name: ListTeamMembers :many
SELECT m.team_id, m.user_id FROM event_team_members AS m
JOIN event_rsvps AS r ON r.event_id = m.event_id AND r.user_id = m.user_id
JOIN events AS e ON e.id = m.event_id
JOIN memberships AS ms ON ms.tayfa_id = e.tayfa_id AND ms.user_id = m.user_id AND ms.status = 'active'
WHERE m.event_id = @event_id AND m.component_key = @component_key
  AND (r.status = 'going' OR r.attendance = 'attended')
ORDER BY m.created_at, m.user_id;

-- Takıma alınabilecek kişiler: tayfanın etkin üyesi olan ve "geliyorum"
-- demiş ya da katılımı doğrulanmış katılımcılar, kimlik sırasıyla.
-- name: ListTeamCandidates :many
SELECT r.user_id FROM event_rsvps AS r
JOIN events AS e ON e.id = r.event_id
JOIN memberships AS ms ON ms.tayfa_id = e.tayfa_id AND ms.user_id = r.user_id AND ms.status = 'active'
WHERE r.event_id = @event_id AND (r.status = 'going' OR r.attendance = 'attended')
ORDER BY r.user_id;

-- Takımı sırasında yazar; o sırada takım varsa kimliği korunur, adı
-- değişir.
-- name: UpsertTeam :one
INSERT INTO event_teams (id, event_id, component_key, position, name, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @position, @name, @now, @now)
ON CONFLICT (event_id, component_key, position)
DO UPDATE SET name = EXCLUDED.name, updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: DeleteTeamsFrom :exec
DELETE FROM event_teams
WHERE event_id = @event_id AND component_key = @component_key AND position >= @from_position;

-- name: DeleteTeamMembers :exec
DELETE FROM event_team_members
WHERE event_id = @event_id AND component_key = @component_key;

-- name: AddTeamMember :exec
INSERT INTO event_team_members (event_id, component_key, team_id, user_id, created_at)
VALUES (@event_id, @component_key, @team_id, @user_id, @created_at);
