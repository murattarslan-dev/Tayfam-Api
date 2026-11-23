-- Oylama bileşeni (`vote`; issue #254; ADR-0025 §4, §5; ADR-0037 §3).
-- Etkinlik her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin
-- tür sürümünde çözülür (internal/core/app/component: View / Change);
-- buradaki sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır
-- (IDOR). Yetki, kaynağa göre seçenekler, oy gizliliği ve sonucun ne zaman
-- görüneceği app katmanındadır (internal/core/app/component/vote): kimin
-- neye oy verdiği istemciye hiçbir zaman dönmez.

-- Serbest seçenekler, sırasıyla.
-- name: ListVoteOptions :many
SELECT id, label FROM event_vote_options
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY position, id;

-- name: CountVoteOptions :one
SELECT count(*) FROM event_vote_options
WHERE event_id = @event_id AND component_key = @component_key;

-- name: VoteOptionLabelTaken :one
SELECT EXISTS (
  SELECT 1 FROM event_vote_options
  WHERE event_id = @event_id AND component_key = @component_key AND label_normalized = @label_normalized
)::boolean;

-- Etkinlik satırı kilitliyken okunur (component.Service.Change).
-- name: NextVoteOptionPosition :one
SELECT (COALESCE(max(position), 0) + 1)::smallint FROM event_vote_options
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CreateVoteOption :exec
INSERT INTO event_vote_options (id, event_id, component_key, label, label_normalized, position, created_by, created_at)
VALUES (@id, @event_id, @component_key, @label, @label_normalized, @position, @created_by, @created_at);

-- Seçeneğe verilen oylar yabancı anahtarla birlikte silinir.
-- name: DeleteVoteOption :execrows
DELETE FROM event_vote_options
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- participants kaynağının seçenekleri: tayfanın etkin üyesi olan ve
-- "geliyorum" demiş ya da katılımı doğrulanmış katılımcılar, kimlik sırasıyla.
-- name: ListVoteCandidates :many
SELECT r.user_id FROM event_rsvps AS r
JOIN events AS e ON e.id = r.event_id
JOIN memberships AS ms ON ms.tayfa_id = e.tayfa_id AND ms.user_id = r.user_id AND ms.status = 'active'
WHERE r.event_id = @event_id AND (r.status = 'going' OR r.attendance = 'attended')
ORDER BY r.user_id;

-- vehicles kaynağının seçenekleri: aynı türdeki araçlar bileşenine yazılmış
-- araçlar, yazılış sırasıyla. Plaka okunmaz.
-- name: ListVoteVehicles :many
SELECT id, user_id, make, model, model_year, color FROM event_vehicles
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY created_at, id;

-- album kaynağının seçenekleri: albümün silinmemiş fotoğrafları, eklenme
-- sırasıyla (albüm başına en fazla 500).
-- name: ListVotePhotos :many
SELECT id, uploader_id, upload_id FROM event_photos
WHERE event_id = @event_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- Bileşenin oyları. Sayım ve kişinin kendi oyu app katmanında çıkarılır;
-- oy verenler istemciye dönmez.
-- name: ListEventVotes :many
SELECT user_id, option_id, candidate_user_id, event_vehicle_id, photo_id FROM event_votes
WHERE event_id = @event_id AND component_key = @component_key;

-- Kişi başına bir oy; yeniden vermek öncekinin yerine geçer.
-- name: UpsertEventVote :exec
INSERT INTO event_votes (id, event_id, component_key, user_id, option_id, candidate_user_id, event_vehicle_id, photo_id, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @user_id, @option_id, @candidate_user_id, @event_vehicle_id, @photo_id, @now, @now)
ON CONFLICT (event_id, component_key, user_id) DO UPDATE SET
  option_id = EXCLUDED.option_id, candidate_user_id = EXCLUDED.candidate_user_id,
  event_vehicle_id = EXCLUDED.event_vehicle_id, photo_id = EXCLUDED.photo_id, updated_at = EXCLUDED.updated_at;

-- name: DeleteEventVote :execrows
DELETE FROM event_votes
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id;

-- name: GetVoteClosure :one
SELECT closed_at FROM event_vote_closures
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CloseVote :execrows
INSERT INTO event_vote_closures (event_id, component_key, closed_by, closed_at)
VALUES (@event_id, @component_key, @closed_by, @closed_at)
ON CONFLICT (event_id, component_key) DO NOTHING;
