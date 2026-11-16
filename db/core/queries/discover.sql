-- Keşfet ilk aşama (issue #210, ADR-0033). Listelenebilirlik
-- tayfa_discover_listable, "görünür arkadaş" discover_friend_visible
-- (migration 00028) ile tek yerde tanımlıdır. Arkadaşlık puanı yalnızca
-- sıralamada kullanılır; hiçbir sorgunun sonucunda yoktur.

-- ---------------------------------------------------------------------------
-- Akış

-- En iyi n etkinlik: görünür arkadaşlarla puan toplamı, görünür arkadaş
-- sayısı, başlangıç, kimlik. Tayfa olguları donmuş etkinliklerden (bitişten
-- 7 gün geçmiş, iptal edilmemiş, silinmemiş; ADR-0026 §1). En çok 3 arkadaş
-- (puan, sonra ad sırasıyla); puan dönmez.
-- name: ListDiscoverFeed :many
WITH me AS (
  SELECT NOT EXISTS (SELECT 1 FROM social_settings s WHERE s.user_id = @viewer::uuid AND NOT s.profiling_enabled) AS profiling
), vis AS (
  SELECT r.event_id, r.user_id AS friend_id,
         CASE WHEN me.profiling
                   AND NOT EXISTS (SELECT 1 FROM social_settings s WHERE s.user_id = r.user_id AND NOT s.profiling_enabled)
              THEN COALESCE(fs.score, 0) ELSE 0 END::float8 AS score
  FROM friendships f
  CROSS JOIN me
  JOIN event_rsvps r ON r.user_id = CASE WHEN f.user_low_id = @viewer::uuid THEN f.user_high_id ELSE f.user_low_id END
                    AND (r.status = 'going' OR r.attendance = 'attended')
  LEFT JOIN friend_scores fs ON fs.user_low_id = f.user_low_id AND fs.user_high_id = f.user_high_id
  WHERE (f.user_low_id = @viewer::uuid OR f.user_high_id = @viewer::uuid) AND f.status = 'accepted'
    AND discover_friend_visible(@viewer::uuid, r.user_id, r.event_id, @now::timestamptz)
), ranked AS (
  SELECT vis.event_id, count(*) AS n,
         row_number() OVER (ORDER BY sum(vis.score) DESC, count(*) DESC, min(e.starts_at), vis.event_id) AS pos
  FROM vis
  JOIN events e ON e.id = vis.event_id
  GROUP BY vis.event_id
)
SELECT e.id AS event_id, e.type, e.title, e.starts_at, e.ends_at, e.city AS event_city, e.district AS event_district,
       t.id AS tayfa_id, t.name AS tayfa_name, t.description AS tayfa_description, t.city AS tayfa_city,
       t.created_at AS tayfa_created_at,
       ranked.n::integer AS friend_count,
       (SELECT count(*) FROM events x
        WHERE x.tayfa_id = t.id AND x.deleted_at IS NULL AND x.status <> 'cancelled'
          AND x.ends_at <= @frozen_before::timestamptz)::integer AS event_count,
       (SELECT count(*) FROM event_rsvps a JOIN events x ON x.id = a.event_id
        WHERE x.tayfa_id = t.id AND x.deleted_at IS NULL AND x.status <> 'cancelled'
          AND x.ends_at <= @frozen_before::timestamptz AND a.attendance = 'attended')::integer AS attendance_count,
       (SELECT COALESCE(jsonb_agg(jsonb_build_object('user_id', b.friend_id, 'display_name', b.display_name,
                                                     'avatar_upload_id', b.avatar_upload_id)
                                  ORDER BY b.score DESC, b.display_name, b.friend_id), '[]'::jsonb)
        FROM (SELECT v.friend_id, v.score, p.display_name, p.avatar_upload_id
              FROM vis v JOIN user_profiles p ON p.user_id = v.friend_id
              WHERE v.event_id = e.id
              ORDER BY v.score DESC, p.display_name, v.friend_id
              LIMIT 3) b)::jsonb AS friends
FROM ranked
JOIN events e ON e.id = ranked.event_id
JOIN tayfas t ON t.id = e.tayfa_id
WHERE ranked.pos <= @n::bigint
ORDER BY ranked.pos;

-- Tayfa şu anda izleyicinin akışında mı (Keşfet'ten katılım): tayfanın
-- bir etkinliğinde görünür bir arkadaş var.
-- name: DiscoverTayfaVisible :one
SELECT EXISTS (
  SELECT 1
  FROM friendships f
  JOIN event_rsvps r ON r.user_id = CASE WHEN f.user_low_id = @viewer::uuid THEN f.user_high_id ELSE f.user_low_id END
  JOIN events e ON e.id = r.event_id AND e.tayfa_id = @tayfa_id::uuid
  WHERE (f.user_low_id = @viewer::uuid OR f.user_high_id = @viewer::uuid) AND f.status = 'accepted'
    AND discover_friend_visible(@viewer::uuid, r.user_id, r.event_id, @now::timestamptz)
)::boolean;

-- ---------------------------------------------------------------------------
-- Tayfa yöneticisinin ayarı

-- masked_*: yöneticilerin göreceği önceki durum (ADR-0033 §7).
-- name: GetTayfaDiscoverState :one
SELECT t.visibility, d.status AS review_status, d.masked_status, d.masked_until
FROM tayfas t
LEFT JOIN tayfa_discover d ON d.tayfa_id = t.id
WHERE t.id = @tayfa_id;

-- name: SetTayfaVisibility :execrows
UPDATE tayfas SET visibility = @visibility
WHERE id = @id AND deleted_at IS NULL AND visibility <> @visibility;

-- Açılışta başvuru: satır yoksa pending açılır; reddedilmişse yeniden
-- pending olur. Onaylı, bekleyen ve düşürülmüş başvuru değişmez.
-- name: RequestTayfaDiscover :exec
INSERT INTO tayfa_discover (tayfa_id, status, requested_at, requested_by, status_changed_at)
VALUES (@tayfa_id, 'pending', @now, @requested_by, @now)
ON CONFLICT (tayfa_id) DO UPDATE
SET status = 'pending', requested_at = EXCLUDED.requested_at, requested_by = EXCLUDED.requested_by,
    status_changed_at = EXCLUDED.status_changed_at
WHERE tayfa_discover.status = 'rejected';

-- ---------------------------------------------------------------------------
-- Eski Keşfet üyesi (ADR-0033 §7)

-- Kişi tayfaya Keşfet'ten gelmiş ve aktif üyeliği left_after'dan sonra
-- bitmiş mi; tayfa silinmemiş. Tayfa içeriğine erişim vermez: yalnızca
-- tayfayı şikâyet ve katıldığı etkinliğe geri bildirim.
-- name: DiscoverFormerMember :one
SELECT EXISTS (
  SELECT 1 FROM discover_joins j JOIN tayfas t ON t.id = j.tayfa_id
  WHERE j.tayfa_id = @tayfa_id AND j.user_id = @user_id
    AND j.left_at > @left_after::timestamptz AND t.deleted_at IS NULL
)::boolean;

-- Yönetici, Keşfet'ten gelmiş bir üyeyi geri bildirim penceresi açıkken
-- (going ya da attended olduğu etkinliğin bitişi window_start'tan sonra)
-- çıkardı ya da banladı: tayfa incelemeye düşer. Yönetici kendi işlemini
-- bildiği için durum maskelenmez.
-- name: DropDiscoverForRemovedMember :execrows
UPDATE tayfa_discover d
SET status = 'dropped', drop_reason = 'member_removed', status_changed_at = @now::timestamptz
WHERE d.tayfa_id = @tayfa_id::uuid AND d.status IN ('approved', 'pending')
  AND EXISTS (SELECT 1 FROM discover_joins j WHERE j.tayfa_id = d.tayfa_id AND j.user_id = @user_id::uuid)
  AND EXISTS (SELECT 1 FROM event_rsvps r JOIN events e ON e.id = r.event_id
              WHERE e.tayfa_id = d.tayfa_id AND r.user_id = @user_id::uuid AND e.deleted_at IS NULL
                AND (r.status = 'going' OR r.attendance = 'attended')
                AND e.ends_at > @window_start::timestamptz);

-- ---------------------------------------------------------------------------
-- "Her şey yolunda mıydı?"

-- name: GetEventFeedback :one
SELECT answer, updated_at FROM event_feedback
WHERE event_id = @event_id AND user_id = @user_id;

-- name: UpsertEventFeedback :one
INSERT INTO event_feedback (event_id, user_id, answer)
VALUES (@event_id, @user_id, @answer)
ON CONFLICT (event_id, user_id) DO UPDATE SET answer = EXCLUDED.answer
RETURNING answer, updated_at;

-- ---------------------------------------------------------------------------
-- Güvenlik kişisi

-- name: GetSafetyContact :one
SELECT c.contact_user_id, c.grace_minutes, c.returned_at, c.alert_sent_at,
       p.display_name, p.avatar_upload_id
FROM event_safety_contacts c
LEFT JOIN user_profiles p ON p.user_id = c.contact_user_id AND p.deleted_at IS NULL
WHERE c.event_id = @event_id AND c.user_id = @user_id;

-- name: GetSafetyContactForUpdate :one
SELECT contact_user_id, grace_minutes, returned_at, alert_sent_at
FROM event_safety_contacts
WHERE event_id = @event_id AND user_id = @user_id
FOR UPDATE;

-- Kişi güvenlik kişisi olabilir mi: kabul edilmiş arkadaş, iki yönden engel
-- yok, hesap silinmemiş ve platformda banlı değil.
-- name: SafetyContactAllowed :one
SELECT EXISTS (
  SELECT 1 FROM friendships f
  WHERE f.user_low_id = LEAST(@user_id::uuid, @contact_id::uuid)
    AND f.user_high_id = GREATEST(@user_id::uuid, @contact_id::uuid)
    AND f.status = 'accepted'
) AND NOT EXISTS (
  SELECT 1 FROM user_blocks ub
  WHERE (ub.blocker_id = @user_id::uuid AND ub.blocked_id = @contact_id::uuid)
     OR (ub.blocker_id = @contact_id::uuid AND ub.blocked_id = @user_id::uuid)
) AND NOT EXISTS (
  SELECT 1 FROM user_profiles p WHERE p.user_id = @contact_id::uuid AND p.deleted_at IS NOT NULL
) AND NOT EXISTS (
  SELECT 1 FROM user_platform_bans b WHERE b.user_id = @contact_id::uuid AND b.banned_at IS NOT NULL
);

-- Alarm gittiyse değişmez (0 satır).
-- name: UpsertSafetyContact :execrows
INSERT INTO event_safety_contacts (event_id, user_id, contact_user_id, grace_minutes)
VALUES (@event_id, @user_id, @contact_user_id, @grace_minutes)
ON CONFLICT (event_id, user_id) DO UPDATE
SET contact_user_id = EXCLUDED.contact_user_id, grace_minutes = EXCLUDED.grace_minutes
WHERE event_safety_contacts.alert_sent_at IS NULL;

-- Kişinin kendi satırı, etkinlik bu tayfanınsa. Kaldırma ve "döndüm"
-- yalnızca kendi satırına dokunur; üyelik, modül ve bayrak aranmaz
-- (ADR-0033 §10).
-- name: GetOwnSafetyContactForUpdate :one
SELECT c.alert_sent_at
FROM event_safety_contacts c
JOIN events e ON e.id = c.event_id
WHERE c.event_id = @event_id AND c.user_id = @user_id AND e.tayfa_id = @tayfa_id
FOR UPDATE OF c;

-- name: DeleteSafetyContact :exec
DELETE FROM event_safety_contacts WHERE event_id = @event_id AND user_id = @user_id;

-- İlk "döndüm" zamanı korunur.
-- name: MarkSafetyReturned :execrows
UPDATE event_safety_contacts SET returned_at = COALESCE(returned_at, @now)
WHERE event_id = @event_id AND user_id = @user_id;

-- Kişiyi güvenlik kişisi seçenlerin etkinlikleri: bitişinden 24 saat
-- geçmemiş ya da alarmı son 7 günde gitmiş. Koordinat seçilmez. Paylaşım
-- yalnızca hâlâ geçerliyse görünür: ikisi arkadaş, engel yok, katılımcı
-- tayfanın aktif üyesi ve etkinliğe going; etkinlik silinmemiş ya da
-- iptal edilmemiş, tayfa silinmemiş ya da askıda değil. Alarm gitmişse
-- (etkinlik sonradan iptal ya da silinmiş olsa bile) katılımcı check-in
-- yaptıysa görünür; alarmın kendisi bildirimle gitti.
-- name: ListSafetyShares :many
SELECT c.user_id, p.display_name, p.avatar_upload_id, t.name AS tayfa_name,
       e.id AS event_id, e.title, e.starts_at, e.ends_at, e.meeting_point_name, e.city, e.district,
       c.grace_minutes, c.returned_at, c.alert_sent_at
FROM event_safety_contacts c
JOIN events e ON e.id = c.event_id
JOIN tayfas t ON t.id = e.tayfa_id
LEFT JOIN user_profiles p ON p.user_id = c.user_id AND p.deleted_at IS NULL
JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = c.user_id AND m.status = 'active'
JOIN event_rsvps r ON r.event_id = c.event_id AND r.user_id = c.user_id
WHERE c.contact_user_id = @contact_id
  AND (e.ends_at > @ended_after::timestamptz
       OR c.alert_sent_at > @alerted_after::timestamptz)
  AND t.deleted_at IS NULL AND t.deletion_requested_at IS NULL AND t.suspended_at IS NULL
  AND ((r.status = 'going' AND e.status = 'scheduled' AND e.deleted_at IS NULL)
       OR (c.alert_sent_at IS NOT NULL AND r.checked_in_at IS NOT NULL))
  AND EXISTS (SELECT 1 FROM friendships f
              WHERE f.user_low_id = LEAST(c.user_id, c.contact_user_id)
                AND f.user_high_id = GREATEST(c.user_id, c.contact_user_id)
                AND f.status = 'accepted')
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = c.user_id AND ub.blocked_id = c.contact_user_id)
                     OR (ub.blocker_id = c.contact_user_id AND ub.blocked_id = c.user_id))
ORDER BY e.starts_at DESC, e.id
LIMIT 50;

-- ---------------------------------------------------------------------------
-- safety-alerts işi

-- Alarmı gelmiş satırlar: döndüm yok, alarm gitmemiş, bitiş + tolerans
-- geçmiş; katılımcı tayfanın aktif üyesi. Planlı ve silinmemiş etkinlikte
-- going olmalı; etkinlik iptal ya da silindiyse yalnızca check-in
-- yapılmışsa (yola çıkmış demektir). Gönderim anında ikisi hâlâ arkadaş ve
-- engel yok.
-- name: LockDueSafetyAlerts :many
SELECT c.event_id, c.user_id, c.contact_user_id
FROM event_safety_contacts c
JOIN events e ON e.id = c.event_id
JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = c.user_id AND m.status = 'active'
JOIN event_rsvps r ON r.event_id = c.event_id AND r.user_id = c.user_id
WHERE c.returned_at IS NULL AND c.alert_sent_at IS NULL
  AND e.ends_at + make_interval(mins => c.grace_minutes) <= @now::timestamptz
  AND ((r.status = 'going' AND e.status = 'scheduled' AND e.deleted_at IS NULL)
       OR r.checked_in_at IS NOT NULL)
  AND EXISTS (SELECT 1 FROM friendships f
              WHERE f.user_low_id = LEAST(c.user_id, c.contact_user_id)
                AND f.user_high_id = GREATEST(c.user_id, c.contact_user_id)
                AND f.status = 'accepted')
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = c.user_id AND ub.blocked_id = c.contact_user_id)
                     OR (ub.blocker_id = c.contact_user_id AND ub.blocked_id = c.user_id))
ORDER BY e.ends_at, c.event_id, c.user_id
LIMIT @n
FOR UPDATE OF c SKIP LOCKED;

-- name: MarkSafetyAlertSent :execrows
UPDATE event_safety_contacts SET alert_sent_at = @now
WHERE event_id = @event_id AND user_id = @user_id AND alert_sent_at IS NULL AND returned_at IS NULL;

-- ---------------------------------------------------------------------------
-- Saklama ve hesap silme

-- name: DeleteOldSafetyContacts :execrows
DELETE FROM event_safety_contacts c
USING events e
WHERE e.id = c.event_id AND e.ends_at < @before::timestamptz;

-- name: DeleteOldEventFeedback :execrows
DELETE FROM event_feedback WHERE created_at < @before::timestamptz;

-- İşaret ayrılıştan 30 gün sonra gerekmez.
-- name: DeleteOldDiscoverJoins :execrows
DELETE FROM discover_joins WHERE left_at < @before::timestamptz;

-- name: DeleteUserDiscoverJoins :exec
DELETE FROM discover_joins WHERE user_id = @user_id;

-- name: DeleteUserEventFeedback :exec
DELETE FROM event_feedback WHERE user_id = @user_id;

-- name: DeleteUserSafetyContacts :exec
DELETE FROM event_safety_contacts WHERE user_id = @user_id OR contact_user_id = @user_id;

-- name: ClearTayfaDiscoverUser :exec
UPDATE tayfa_discover
SET requested_by = CASE WHEN requested_by = @user_id THEN NULL ELSE requested_by END,
    reviewed_by = CASE WHEN reviewed_by = @user_id THEN NULL ELSE reviewed_by END
WHERE requested_by = @user_id OR reviewed_by = @user_id;

-- ---------------------------------------------------------------------------
-- Platform paneli

-- name: AdminListDiscoverQueue :many
SELECT t.id, t.name, t.description, t.city, t.district, t.created_at, t.visibility,
       d.status, d.drop_reason, d.requested_at, d.status_changed_at,
       (SELECT count(*) FROM memberships m WHERE m.tayfa_id = t.id AND m.status = 'active')::integer AS member_count,
       (SELECT count(*) FROM memberships m
        WHERE m.tayfa_id = t.id AND m.status = 'active' AND m.joined_via = 'discover')::integer AS discover_member_count,
       (SELECT count(*) FROM reports r
        WHERE r.tayfa_id = t.id AND r.platform_status = 'open')::integer AS open_platform_reports,
       (SELECT count(*) FROM event_feedback f JOIN events e ON e.id = f.event_id
        WHERE e.tayfa_id = t.id AND f.answer = 'not_ok' AND f.updated_at > @since::timestamptz)::integer AS not_ok_count,
       (SELECT count(*) FROM event_feedback f JOIN events e ON e.id = f.event_id
        WHERE e.tayfa_id = t.id AND f.answer = 'serious' AND f.updated_at > @since::timestamptz)::integer AS serious_count
FROM tayfa_discover d
JOIN tayfas t ON t.id = d.tayfa_id
WHERE d.status IN ('pending', 'dropped') AND t.deleted_at IS NULL
ORDER BY d.status_changed_at, t.id
LIMIT 200;

-- name: AdminLockTayfaDiscover :one
SELECT status FROM tayfa_discover WHERE tayfa_id = @tayfa_id FOR UPDATE;

-- Panel kararı maskeyi kaldırır.
-- name: AdminApproveDiscover :execrows
UPDATE tayfa_discover
SET status = 'approved', drop_reason = NULL, reviewed_at = @now, reviewed_by = @by, status_changed_at = @now,
    masked_status = NULL, masked_until = NULL
WHERE tayfa_id = @tayfa_id AND status IN ('pending', 'dropped');

-- name: AdminRejectDiscover :execrows
UPDATE tayfa_discover
SET status = 'rejected', drop_reason = NULL, reviewed_at = @now, reviewed_by = @by, status_changed_at = @now,
    masked_status = NULL, masked_until = NULL
WHERE tayfa_id = @tayfa_id AND status <> 'rejected';

-- ---------------------------------------------------------------------------
-- KVKK dışa aktarma (ADR-0021): tek kayıt.

-- name: ExportDiscover :many
SELECT @user_id::uuid AS k,
       jsonb_build_object(
         'feedback', COALESCE((SELECT jsonb_agg(jsonb_build_object(
             'event_id', f.event_id, 'answer', f.answer, 'created_at', f.created_at, 'updated_at', f.updated_at)
             ORDER BY f.created_at, f.event_id)
           FROM event_feedback f WHERE f.user_id = @user_id::uuid), '[]'::jsonb),
         'safety_contacts', COALESCE((SELECT jsonb_agg(jsonb_build_object(
             'event_id', c.event_id, 'contact_user_id', c.contact_user_id, 'grace_minutes', c.grace_minutes,
             'returned_at', c.returned_at, 'alert_sent_at', c.alert_sent_at, 'created_at', c.created_at)
             ORDER BY c.created_at, c.event_id)
           FROM event_safety_contacts c WHERE c.user_id = @user_id::uuid), '[]'::jsonb),
         'discover_joins', COALESCE((SELECT jsonb_agg(jsonb_build_object(
             'tayfa_id', j.tayfa_id, 'joined_at', j.joined_at, 'left_at', j.left_at)
             ORDER BY j.joined_at, j.tayfa_id)
           FROM discover_joins j WHERE j.user_id = @user_id::uuid), '[]'::jsonb)
       ) AS data
FROM (SELECT 1) one
WHERE @user_id::uuid > @after::uuid
  AND (EXISTS (SELECT 1 FROM event_feedback f WHERE f.user_id = @user_id::uuid)
       OR EXISTS (SELECT 1 FROM event_safety_contacts c WHERE c.user_id = @user_id::uuid)
       OR EXISTS (SELECT 1 FROM discover_joins j WHERE j.user_id = @user_id::uuid))
LIMIT @n;
