-- Yazboz bileşeni (`scoreboard`; issue #250; ADR-0025 §4–§6, ADR-0037 §5).
-- Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin
-- tür sürümünde çözülür (internal/core/app/component: View / Change);
-- buradaki sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır
-- (IDOR). Yetki app katmanındadır (internal/core/app/component/scoreboard).

-- Ön ayarlar; emekliye ayrılanlar dahil (eski masalar adlarını gösterir).
-- name: ListScorePresets :many
SELECT key, labels, params, status FROM catalog_score_presets
WHERE key = ANY(@keys::text[])
ORDER BY position, key;

-- name: ListScoreTables :many
SELECT * FROM event_score_tables
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY created_at, id;

-- Masayı kilitleyerek okur: aynı masaya eşzamanlı iki el aynı numarayı
-- alamaz.
-- name: GetScoreTableForUpdate :one
SELECT * FROM event_score_tables
WHERE id = @id AND event_id = @event_id AND component_key = @component_key
FOR UPDATE;

-- name: CountScoreTables :one
SELECT count(*) FROM event_score_tables
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CreateScoreTable :exec
INSERT INTO event_score_tables (id, event_id, component_key, label, preset_key, side, rounds,
  winner, result, target, best_of, team_key, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @label, sqlc.narg(preset_key), @side, @rounds,
  @winner, @result, sqlc.narg(target), sqlc.narg(best_of), sqlc.narg(team_key), @now, @now);

-- name: UpdateScoreTable :exec
UPDATE event_score_tables
SET label = @label, status = @status, finished_at = sqlc.narg(finished_at), updated_at = @now
WHERE id = @id AND event_id = @event_id AND component_key = @component_key;

-- name: DeleteScoreTable :execrows
DELETE FROM event_score_tables
WHERE id = @id AND event_id = @event_id AND component_key = @component_key;

-- Bileşenin bütün masalarındaki yerler.
-- name: ListScorePlayers :many
SELECT id, table_id, position, user_id, team_id FROM event_score_players
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY table_id, position;

-- name: DeleteScorePlayers :exec
DELETE FROM event_score_players
WHERE table_id = @table_id AND event_id = @event_id AND component_key = @component_key;

-- name: AddScorePlayer :exec
INSERT INTO event_score_players (id, table_id, event_id, component_key, position, user_id,
  team_key, team_id, created_at)
VALUES (@id, @table_id, @event_id, @component_key, @position, sqlc.narg(user_id),
  sqlc.narg(team_key), sqlc.narg(team_id), @created_at);

-- Bileşenin bütün masalarındaki skorlar.
-- name: ListScores :many
SELECT table_id, player_id, round, points, outcome FROM event_scores
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY table_id, round, player_id;

-- name: CountTableScores :one
SELECT count(*) FROM event_scores
WHERE table_id = @table_id AND event_id = @event_id AND component_key = @component_key;

-- name: LastScoreRound :one
SELECT COALESCE(max(round), 0)::int AS round FROM event_scores
WHERE table_id = @table_id AND event_id = @event_id AND component_key = @component_key;

-- Düzeltme eli siler ve yeniden yazar.
-- name: DeleteScoreRound :execrows
DELETE FROM event_scores
WHERE table_id = @table_id AND event_id = @event_id AND component_key = @component_key
  AND round = @round;

-- name: AddScore :exec
INSERT INTO event_scores (id, table_id, event_id, component_key, player_id, round, points,
  outcome, created_at)
VALUES (@id, @table_id, @event_id, @component_key, @player_id, @round, sqlc.narg(points),
  sqlc.narg(outcome), @created_at);
