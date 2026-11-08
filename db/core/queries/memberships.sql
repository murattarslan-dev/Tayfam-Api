-- Üyelik değişiklikleri transaction içinde SetActor ya da SetSystemJob'dan
-- sonra yapılır; aksi halde trigger reddeder.

-- name: CreateMembership :exec
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, invite_id, invited_by_user_id, joined_at)
VALUES (@tayfa_id, @user_id, @role, @status, @joined_via, @invite_id, @invited_by_user_id, @joined_at);

-- name: GetMembership :one
SELECT tayfa_id, user_id, role, status, joined_via, invite_id,
       invited_by_user_id, approved_by, muted_until, muted_by, joined_at, created_at
FROM memberships
WHERE tayfa_id = @tayfa_id AND user_id = @user_id;

-- Duruma göre üyeler; kullanıcı ID'sine göre sayfalanır.
-- name: ListMemberships :many
SELECT m.user_id, m.role, m.status, m.joined_via, m.invite_id,
       m.invited_by_user_id, i.created_by AS invite_created_by,
       m.muted_until, m.joined_at, m.created_at
FROM memberships m
LEFT JOIN invites i ON i.id = m.invite_id
WHERE m.tayfa_id = @tayfa_id
  AND m.status = @status
  AND m.user_id > @after_user_id
ORDER BY m.user_id
LIMIT @page_size;

-- name: CountActiveMembers :one
SELECT count(*) FROM memberships WHERE tayfa_id = @tayfa_id AND status = 'active';

-- name: SetMembershipRole :execrows
UPDATE memberships SET role = @role
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'active';

-- Başvuru onayı.
-- name: ApproveMembership :execrows
UPDATE memberships SET status = 'active', approved_by = @approved_by, joined_at = now()
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'pending';

-- Doğrudan davetin kabulü; status active ya da pending.
-- name: AcceptInvitation :execrows
UPDATE memberships SET status = @status,
  joined_at = CASE WHEN @status::text = 'active' THEN now() END
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'invited';

-- name: BanMembership :execrows
UPDATE memberships SET status = 'banned', muted_until = NULL, muted_by = NULL
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status <> 'banned';

-- name: MuteMembership :execrows
UPDATE memberships SET muted_until = @muted_until, muted_by = @muted_by
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'active';

-- name: UnmuteMembership :execrows
UPDATE memberships SET muted_until = NULL, muted_by = NULL
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND muted_until IS NOT NULL;

-- Ayrılma, çıkarma, başvuru reddi, ban kaldırma.
-- name: DeleteMembership :execrows
DELETE FROM memberships WHERE tayfa_id = @tayfa_id AND user_id = @user_id;
