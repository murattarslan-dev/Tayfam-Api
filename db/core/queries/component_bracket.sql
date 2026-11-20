-- Eşleşme ağacı bileşeni (`bracket`; issue #251; ADR-0025 §4). Etkinlik
-- her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür
-- sürümünde çözülür (internal/core/app/component: View / Change); buradaki
-- sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR).
-- Yetki app katmanındadır (internal/core/app/component/bracket).

-- name: GetBracket :one
SELECT draw_key, format, best_of, result, winner, rounds, started_at FROM event_brackets
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CreateBracket :exec
INSERT INTO event_brackets (event_id, component_key, draw_key, format, best_of, result, winner, rounds, started_at)
VALUES (@event_id, @component_key, @draw_key, @format, @best_of, @result, @winner, @rounds, @started_at);

-- Turnuvayı sıfırlar; maçlar ve oyunlar birlikte gider.
-- name: DeleteBracket :exec
DELETE FROM event_brackets WHERE event_id = @event_id AND component_key = @component_key;

-- name: ListBracketMatches :many
SELECT id, round, slot, a_position, b_position FROM event_bracket_matches
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY round, slot;

-- name: AddBracketMatch :exec
INSERT INTO event_bracket_matches (id, event_id, component_key, draw_key, round, slot, a_position, b_position, created_at)
VALUES (@id, @event_id, @component_key, @draw_key, @round, @slot, @a_position, @b_position, @created_at);

-- name: ListBracketGames :many
SELECT match_id, number, a_points, b_points, a_outcome FROM event_bracket_games
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY match_id, number;

-- name: AddBracketGame :exec
INSERT INTO event_bracket_games (match_id, event_id, component_key, number, a_points, b_points, a_outcome, created_at)
VALUES (@match_id, @event_id, @component_key, @number, @a_points, @b_points, @a_outcome, @created_at);

-- name: DeleteBracketGame :execrows
DELETE FROM event_bracket_games
WHERE match_id = @match_id AND event_id = @event_id AND component_key = @component_key AND number = @number;
