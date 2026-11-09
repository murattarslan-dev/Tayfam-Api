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
