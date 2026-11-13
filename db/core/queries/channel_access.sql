-- Etkinlik ve seçili üye kanalları (issue #183, ADR-0024). chat bu
-- sorguların cevabını çağıranın token'ıyla sorar; katılımın kopyası chat'te
-- tutulmaz. Kitle yalnızca etkin üyelerden oluşur.

-- Etkinlikler, çağıranın cevabıyla. Başka tayfanın ya da silinmiş etkinlik
-- satırı dönmez.
-- name: ChannelEvents :many
SELECT e.id, e.ends_at, e.status, e.cancelled_at, e.created_by,
       COALESCE((SELECT r.status FROM event_rsvps r WHERE r.event_id = e.id AND r.user_id = @caller_id), '')::text AS caller_status
FROM events e
WHERE e.tayfa_id = @tayfa_id AND e.id = ANY(@event_ids::uuid[]) AND e.deleted_at IS NULL;

-- Her etkinlik için gidecekler ve organizatör (etkin üyeler, her biri bir
-- kez); kanalı açan Go'da eklenir.
-- name: EventAudienceCounts :many
SELECT a.event_id, count(*)::integer AS member_count FROM (
  SELECT r.event_id, r.user_id FROM event_rsvps r WHERE r.event_id = ANY(@event_ids::uuid[]) AND r.status = 'going'
  UNION
  SELECT e.id, e.created_by FROM events e WHERE e.id = ANY(@event_ids::uuid[]) AND e.created_by IS NOT NULL
) a
JOIN memberships m ON m.tayfa_id = @tayfa_id AND m.user_id = a.user_id AND m.status = 'active'
GROUP BY a.event_id;

-- Kanalı açanlardan hangileri o etkinliğe gidiyor.
-- name: GoingAmong :many
SELECT event_id, user_id FROM event_rsvps
WHERE event_id = ANY(@event_ids::uuid[]) AND user_id = ANY(@user_ids::uuid[]) AND status = 'going';

-- Etkinlik kanalının kitlesi (gidecekler ve organizatör; kanalı açan Go'da
-- eklenir), kullanıcı kimliğine göre sıralı, imleçten sonrası.
-- name: ListEventAudience :many
WITH audience AS (
  SELECT r.user_id FROM event_rsvps r WHERE r.event_id = @event_id AND r.status = 'going'
  UNION
  SELECT e.created_by FROM events e WHERE e.id = @event_id AND e.created_by IS NOT NULL
)
SELECT m.user_id FROM memberships m
WHERE m.tayfa_id = @tayfa_id AND m.status = 'active'
  AND m.user_id IN (SELECT user_id FROM audience)
  AND m.user_id > @after
ORDER BY m.user_id
LIMIT @page_size;

-- name: FilterActiveMembers :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND user_id = ANY(@user_ids::uuid[]);
