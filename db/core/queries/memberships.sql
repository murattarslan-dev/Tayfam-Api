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

-- Son yönetici kontrolü; çağıran tayfa satırını kilitlemiş olmalıdır.
-- name: CountOtherActiveAdmins :one
SELECT count(*) FROM memberships
WHERE tayfa_id = @tayfa_id AND user_id <> @user_id
  AND status = 'active' AND role = 'admin';

-- Üye listesi: önce rol (yönetici başta), sonra katılım zamanı, sonra
-- kullanıcı ID'si. Katılmadan banlanan kişinin katılım zamanı yoktur; onun
-- yerine başvuru zamanı kullanılır. Sayfa imleci (after_rank, after_time,
-- after_user_id); ilk sayfa için after_rank = 4.
-- name: ListMembersPage :many
SELECT m.user_id, m.role, m.status, m.muted_until, m.joined_at,
       COALESCE(m.joined_at, m.created_at)::timestamptz AS sort_time
FROM memberships m
WHERE m.tayfa_id = @tayfa_id
  AND m.status = @status
  AND (role_rank(m.role) < @after_rank::int
       OR (role_rank(m.role) = @after_rank::int
           AND (COALESCE(m.joined_at, m.created_at), m.user_id) > (@after_time::timestamptz, @after_user_id::uuid)))
ORDER BY role_rank(m.role) DESC, COALESCE(m.joined_at, m.created_at), m.user_id
LIMIT @page_size;

-- Bekleyen başvurular, eskiden yeniye; kimin davetiyle geldiği: doğrudan
-- davet eden ya da davet linkini açan.
-- name: ListPendingPage :many
SELECT m.user_id, m.role, m.joined_via, m.invite_id,
       COALESCE(m.invited_by_user_id, i.created_by)::uuid AS inviter_user_id,
       m.created_at
FROM memberships m
LEFT JOIN invites i ON i.tayfa_id = m.tayfa_id AND i.id = m.invite_id
WHERE m.tayfa_id = @tayfa_id
  AND m.status = 'pending'
  AND (m.created_at, m.user_id) > (@after_time::timestamptz, @after_user_id::uuid)
ORDER BY m.created_at, m.user_id
LIMIT @page_size;

-- Doğrudan davetler (issue #71, v14 "Tayfaya davet").

-- Kişiye gelen bekleyen davetler; silinmiş tayfalarınki görünmez. Davet
-- edilen kişi henüz üye değildir; yalnızca tayfanın adını ve kimin davet
-- ettiğini görür. (created_at, tayfa_id) ile sayfalanır, yeniden eskiye.
-- name: ListMyInvitationsPage :many
SELECT m.tayfa_id, t.name AS tayfa_name, m.invited_by_user_id, m.created_at
FROM memberships m
JOIN tayfas t ON t.id = m.tayfa_id AND t.deleted_at IS NULL
WHERE m.user_id = @user_id AND m.status = 'invited'
  AND (m.created_at, m.tayfa_id) < (@before_time::timestamptz, @before_tayfa_id::uuid)
ORDER BY m.created_at DESC, m.tayfa_id DESC
LIMIT @page_size;

-- Hesap silme: kişinin gönderdiği, henüz cevaplanmamış davetler. Tayfalar
-- kimlik sırasıyla işlenir (kilit sırası).
-- name: ListInvitationsSentByUser :many
SELECT tayfa_id, user_id
FROM memberships
WHERE invited_by_user_id = @user_id AND status = 'invited'
ORDER BY tayfa_id, user_id;

-- Tayfanın bekleyen davetleri (yönetici), yeniden eskiye.
-- name: ListTayfaInvitationsPage :many
SELECT m.user_id, m.invited_by_user_id, m.created_at
FROM memberships m
WHERE m.tayfa_id = @tayfa_id AND m.status = 'invited'
  AND (m.created_at, m.user_id) < (@before_time::timestamptz, @before_user_id::uuid)
ORDER BY m.created_at DESC, m.user_id DESC
LIMIT @page_size;
