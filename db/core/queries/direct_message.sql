-- Birebir mesaj kuralının girdileri (issue #95, veri modeli v14 "Mesaj
-- İsteği Modeli (v7)", ADR-0009 CheckDirectMessage). Engelleme ve
-- arkadaşlık social.sql'deki sorgularla okunur.

-- Gönderenin, alıcıyla ortak olduğu canlı tayfalardaki en eski aktif
-- üyeliğinin başlangıcı; ortak tayfa yoksa NULL. Canlı tayfa: silinmemiş,
-- askıya alınmamış, silme talebi beklemeyen ve arşivlenmemiş tayfa.
-- name: SharedTayfaSenderJoinedAt :one
SELECT min(s.joined_at)::timestamptz AS joined_at
FROM memberships s
JOIN memberships r
  ON r.tayfa_id = s.tayfa_id AND r.user_id = @recipient_id AND r.status = 'active'
JOIN tayfas t
  ON t.id = s.tayfa_id
 AND t.deleted_at IS NULL AND t.suspended_at IS NULL
 AND t.deletion_requested_at IS NULL AND t.status <> 'archived'
WHERE s.user_id = @sender_id AND s.status = 'active';

-- Ortak etkinlik geçmişi: ikisinin de attended işaretlendiği, iptal
-- edilmemiş, silinmemiş ve bitmiş en az bir etkinlik (hangi tayfada
-- olduğu fark etmez).
-- name: HasSharedEventHistory :one
SELECT EXISTS (
  SELECT 1
  FROM event_rsvps a
  JOIN event_rsvps b
    ON b.event_id = a.event_id AND b.user_id = @user_b AND b.attendance = 'attended'
  JOIN events e
    ON e.id = a.event_id AND e.status = 'scheduled' AND e.deleted_at IS NULL AND e.ends_at <= @now
  WHERE a.user_id = @user_a AND a.attendance = 'attended'
);
