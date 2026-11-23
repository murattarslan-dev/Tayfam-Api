-- Bildirim alıcıları (ADR-0009 "Bildirim oluşturma", issue #64). Alıcıları
-- üyeliklerin sahibi core hesaplar ve notification.requested olayıyla
-- chat'e bildirir. Yalnızca etkin üyeler; bekleyen, banlı, ayrılmış ya da
-- hiç üye olmamış kullanıcılar ve işlemi yapan (exclude_user_id) hariç.

-- name: ListNotifyMembers :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND user_id <> @exclude_user_id
ORDER BY user_id;

-- Yönetici ve moderatörler (başvuru, silme talebi).
-- name: ListNotifyStaff :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND role IN ('admin', 'moderator')
  AND user_id <> @exclude_user_id
ORDER BY user_id;

-- Etkinliğe going, maybe ya da waitlist diyen etkin üyeler.
-- name: ListNotifyParticipants :many
SELECT r.user_id FROM event_rsvps r
JOIN memberships m ON m.tayfa_id = @tayfa_id AND m.user_id = r.user_id AND m.status = 'active'
WHERE r.event_id = @event_id AND r.status IN ('going', 'maybe', 'waitlist')
  AND r.user_id <> @exclude_user_id
ORDER BY r.user_id;

-- Verilen kullanıcılardan hâlâ etkin üye olanlar (tek alıcılı bildirimler
-- ve arka plan işlerinin seçtiği alıcılar).
-- name: FilterNotifyActive :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND user_id = ANY(@user_ids::uuid[])
  AND user_id <> @exclude_user_id
ORDER BY user_id;

-- Şimdi bildirimi (issue #265; ADR-0038 §4).

-- Tayfa şimdi etkinliğinin alıcıları: organizatör dışındaki etkin üyeler;
-- organizatörle arasında iki yönden herhangi birinde engel olan hariç.
-- name: ListNotifyNowMembers :many
SELECT m.user_id FROM memberships m
WHERE m.tayfa_id = @tayfa_id AND m.status = 'active' AND m.user_id <> @actor_id
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = @actor_id AND ub.blocked_id = m.user_id)
                     OR (ub.blocker_id = m.user_id AND ub.blocked_id = @actor_id))
ORDER BY m.user_id;

-- Anlık şimdi etkinliğinde organizatörün seçtiği alıcılardan bildirim
-- alabilecek olanlar: gönderenin kabul edilmiş arkadaşı, iki yönden engel
-- yok, hesabı silinmemiş ve platform banlı değil, alıcının
-- now_from_friends ayarı izin veriyor (all; close'ta gönderen alıcının
-- ilk 20'sinde ya da alıcının profillemesi kapalı; off hiç).
-- name: FilterNowFriendRecipients :many
SELECT p.user_id FROM user_profiles p
JOIN friendships f
  ON f.user_low_id = LEAST(p.user_id, @sender_id::uuid) AND f.user_high_id = GREATEST(p.user_id, @sender_id::uuid)
 AND f.status = 'accepted'
LEFT JOIN social_settings s ON s.user_id = p.user_id
WHERE p.user_id = ANY(@user_ids::uuid[]) AND p.user_id <> @sender_id::uuid AND p.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = p.user_id AND b.banned_at IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = @sender_id::uuid AND ub.blocked_id = p.user_id)
                     OR (ub.blocker_id = p.user_id AND ub.blocked_id = @sender_id::uuid))
  AND (s.user_id IS NULL OR s.now_from_friends = 'all'
       OR (s.now_from_friends = 'close' AND (NOT s.profiling_enabled OR now_close_friend(p.user_id, @sender_id::uuid))))
ORDER BY p.user_id;

-- Günlük sayaçtan bir hak alır; sınır doluysa satır dönmez (pgx.ErrNoRows).
-- name: TakeNowCounter :one
INSERT INTO now_notification_counters AS c (scope, subject_id, day, sent)
VALUES (@scope, @subject_id, @day::date, 1)
ON CONFLICT (scope, subject_id, day) DO UPDATE SET sent = c.sent + 1
WHERE c.sent < @max_sent::integer
RETURNING c.sent;

-- name: DeleteOldNowCounters :execrows
DELETE FROM now_notification_counters WHERE day < @before::date;

-- name: DeleteUserNowCounters :exec
DELETE FROM now_notification_counters WHERE scope = 'user' AND subject_id = @user_id;

-- Ortak kütüphane ödünç adımları (issue #321; ADR-0040 §1): verilen
-- kullanıcılardan etkin üye olanlar; işlemi yapan (actor_id) ve onunla
-- arasında iki yönden herhangi birinde engel olan hariç.
-- name: FilterNotifyUnblocked :many
SELECT m.user_id FROM memberships m
WHERE m.tayfa_id = @tayfa_id AND m.status = 'active' AND m.user_id = ANY(@user_ids::uuid[])
  AND m.user_id <> @actor_id
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = @actor_id AND ub.blocked_id = m.user_id)
                     OR (ub.blocker_id = m.user_id AND ub.blocked_id = @actor_id))
ORDER BY m.user_id;
