-- Rozetler (issue #258; ADR-0039). Tanımlar katalog verisidir ve
-- core_app'e salt okunurdur; kazanılmış rozetler eklenir, değişmez.
-- Kuralların doğrulanması ve kazanma app katmanındadır
-- (internal/core/app/catalog/badges.go, internal/core/app/badge).

-- Bütün tanımlar, emekliler dahil: kazanılmış rozetin adı ve ikonu
-- buradan okunur.
-- name: ListCatalogBadges :many
SELECT key, scope, position, labels, icon, rule, status
FROM catalog_badges
ORDER BY position, key;

-- Bir türün bütün sürümlerinin tanımları: rozet kuralı, türün hiçbir
-- sürümünde hassas olan bir ölçüme ya da hassas katılıma dayanamaz.
-- name: ListCatalogTypeDefinitions :many
SELECT version, definition FROM catalog_event_type_versions
WHERE type_key = @type_key
ORDER BY version;

-- name: AddUserBadge :execrows
INSERT INTO user_badges (id, user_id, badge_key, period_key, tayfa_id, event_id, earned_at)
VALUES (@id, @user_id, @badge_key, @period_key, @tayfa_id, @event_id, @earned_at)
ON CONFLICT DO NOTHING;

-- name: AddTayfaBadge :execrows
INSERT INTO tayfa_badges (id, tayfa_id, badge_key, period_key, earned_at)
VALUES (@id, @tayfa_id, @badge_key, @period_key, @earned_at)
ON CONFLICT DO NOTHING;

-- Kişinin rozetleri, kazanma sırasıyla.
-- name: ListUserBadges :many
SELECT badge_key, period_key, tayfa_id, event_id, earned_at
FROM user_badges
WHERE user_id = @user_id
ORDER BY earned_at, id
LIMIT 1000;

-- Tayfanın rozetleri, kazanma sırasıyla.
-- name: ListTayfaBadges :many
SELECT badge_key, period_key, earned_at
FROM tayfa_badges
WHERE tayfa_id = @tayfa_id
ORDER BY earned_at, id
LIMIT 1000;

-- Kutlaması donmuş ve şampiyonluk rozetlerine henüz bakılmamış
-- etkinlikler: tür sürümünde kutlama bileşeni olan, iptal edilmemiş,
-- silinmemiş, bitişinin üzerinden düzeltme penceresi geçmiş.
-- name: ListChampionBadgeEvents :many
SELECT e.id, e.tayfa_id, e.type, e.type_version
FROM events AS e
JOIN catalog_event_type_versions AS v ON v.type_key = e.type AND v.version = e.type_version
WHERE e.ends_at <= @frozen_before
  AND e.deleted_at IS NULL
  AND e.status <> 'cancelled'
  AND v.definition -> 'components' @> '[{"kind": "celebration"}]'::jsonb
  AND NOT EXISTS (SELECT 1 FROM event_badge_runs AS r WHERE r.event_id = e.id)
ORDER BY e.ends_at, e.id
LIMIT @n;

-- name: MarkEventBadgeRun :exec
INSERT INTO event_badge_runs (event_id, done_at) VALUES (@event_id, @done_at)
ON CONFLICT (event_id) DO NOTHING;

-- Kazanan takımın üyeleri.
-- name: ListBadgeTeamMembers :many
SELECT user_id FROM event_team_members
WHERE event_id = @event_id AND team_id = @team_id
ORDER BY user_id;
