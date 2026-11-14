-- İstek ve davet sonuçları (issue #192). Okuma yalnızca alıcının
-- (doğrulanmış çağıran) kendi kayıtları üzerinedir.

-- name: SetFriendRequestOutcome :exec
INSERT INTO friend_request_outcomes (requester_id, recipient_id, outcome, resolved_at)
VALUES (@requester_id, @recipient_id, @outcome, @resolved_at)
ON CONFLICT (requester_id, recipient_id) DO UPDATE
SET outcome = EXCLUDED.outcome, resolved_at = EXCLUDED.resolved_at;

-- Yeni istek: eski sonuç geçersiz.
-- name: ClearFriendRequestOutcome :exec
DELETE FROM friend_request_outcomes WHERE requester_id = @requester_id AND recipient_id = @recipient_id;

-- name: SetInvitationOutcome :exec
INSERT INTO invitation_outcomes (tayfa_id, user_id, outcome, resolved_at)
VALUES (@tayfa_id, @user_id, @outcome, @resolved_at)
ON CONFLICT (tayfa_id, user_id) DO UPDATE
SET outcome = EXCLUDED.outcome, resolved_at = EXCLUDED.resolved_at;

-- Yeni davet: eski sonuç geçersiz.
-- name: ClearInvitationOutcome :exec
DELETE FROM invitation_outcomes WHERE tayfa_id = @tayfa_id AND user_id = @user_id;

-- Alıcının bir sayfadaki istek bildirimleri için: isteyenlerle arasındaki
-- arkadaşlık satırı ve kayıtlı sonuç.
-- name: FriendRequestStates :many
SELECT u.requester_id::uuid AS requester_id,
       f.status AS friendship_status, f.requested_by,
       o.outcome
FROM unnest(@requester_ids::uuid[]) AS u(requester_id)
LEFT JOIN friendships f
  ON f.user_low_id = LEAST(u.requester_id, @recipient_id::uuid) AND f.user_high_id = GREATEST(u.requester_id, @recipient_id::uuid)
LEFT JOIN friend_request_outcomes o
  ON o.requester_id = u.requester_id AND o.recipient_id = @recipient_id::uuid;

-- Davet edilenin bir sayfadaki davet bildirimleri için: o tayfadaki
-- üyeliği ve kayıtlı sonuç.
-- name: InvitationStates :many
SELECT u.tayfa_id::uuid AS tayfa_id, m.status AS membership_status, o.outcome
FROM unnest(@tayfa_ids::uuid[]) AS u(tayfa_id)
LEFT JOIN memberships m ON m.tayfa_id = u.tayfa_id AND m.user_id = @user_id::uuid
LEFT JOIN invitation_outcomes o ON o.tayfa_id = u.tayfa_id AND o.user_id = @user_id::uuid;

-- name: DeleteUserRequestOutcomes :exec
WITH f AS (DELETE FROM friend_request_outcomes WHERE requester_id = @user_id OR recipient_id = @user_id)
DELETE FROM invitation_outcomes WHERE user_id = @user_id;

-- name: DeleteOldFriendRequestOutcomes :execrows
DELETE FROM friend_request_outcomes WHERE resolved_at < @before;

-- name: DeleteOldInvitationOutcomes :execrows
DELETE FROM invitation_outcomes WHERE resolved_at < @before;
