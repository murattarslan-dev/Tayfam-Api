-- Misafir oyuncu (issue #252; ADR-0037 §6). Etkinlik her zaman önce tayfa
-- kapsamında yüklenir ve yazboz bileşeni etkinliğin tür sürümünde çözülür
-- (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliğiyle çalışır (IDOR). Yetki app katmanındadır
-- (internal/core/app/component/guest).

-- Etkinliğin misafirleri, eklenme sırasıyla. Takma adı silinmiş misafir
-- de listelenir ("Misafir").
-- name: ListGuestPlayers :many
SELECT id, nickname, created_at FROM event_guest_players
WHERE event_id = @event_id
ORDER BY id;

-- name: CountGuestPlayers :one
SELECT count(*) FROM event_guest_players WHERE event_id = @event_id;

-- name: AddGuestPlayer :exec
INSERT INTO event_guest_players (id, event_id, nickname, created_by, created_at)
VALUES (@id, @event_id, @nickname, @created_by, @created_at);

-- Misafir bir masada ya da kurada yer alıyor mu: yer alan misafir
-- silinmez (skor ve kura sabittir).
-- name: GuestPlayerInUse :one
SELECT EXISTS (SELECT 1 FROM event_score_players AS p WHERE p.guest_id = @id AND p.event_id = @event_id)
  OR EXISTS (SELECT 1 FROM event_draw_entries AS d WHERE d.guest_id = @id AND d.event_id = @event_id);

-- name: DeleteGuestPlayer :execrows
DELETE FROM event_guest_players WHERE id = @id AND event_id = @event_id;

-- Misafiri ekleyen hakemin masası bileşenin masası mı.
-- name: GuestScoreTableExists :one
SELECT EXISTS (
  SELECT 1 FROM event_score_tables
  WHERE id = @id AND event_id = @event_id AND component_key = @component_key
);
