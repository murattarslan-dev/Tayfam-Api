-- Arkadaşlık ve engelleme (issue #71, veri modeli v14 "Arkadaşlık ve
-- Davet (v11)"). friendships satırı iki kişi için tektir: user_low_id <
-- user_high_id. Uygulama her sorguya düşük ve yüksek kimliği sıralı verir.

-- name: GetFriendship :one
SELECT user_low_id, user_high_id, status, requested_by, created_at, accepted_at
FROM friendships
WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id;

-- Aynı iki kişinin eşzamanlı istekleri sırayla işlenir.
-- name: LockFriendship :one
SELECT user_low_id, user_high_id, status, requested_by, created_at, accepted_at
FROM friendships
WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id
FOR UPDATE;

-- Satır varsa hiçbir şey yapmaz; etkilenen satır 0 olur.
-- name: CreateFriendRequest :execrows
INSERT INTO friendships (user_low_id, user_high_id, status, requested_by)
VALUES (@user_low_id, @user_high_id, 'pending', @requested_by)
ON CONFLICT DO NOTHING;

-- Yalnızca isteği alan kabul eder.
-- name: AcceptFriendRequest :execrows
UPDATE friendships SET status = 'accepted', accepted_at = now()
WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id
  AND status = 'pending' AND requested_by <> @accepter;

-- Arkadaşlığı bitirme, isteği geri çekme ya da reddetme.
-- name: DeleteFriendship :execrows
DELETE FROM friendships WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id;

-- Kişinin arkadaşları ve istekleri; karşı tarafın kimliğine göre sayfalanır.
-- Silinmiş hesapların satırı hesap silmede gider; profili henüz gelmemiş
-- kişi listede adsız görünmez, atlanır.
-- name: ListFriendshipsPage :many
SELECT p.user_id AS other_id, f.status, f.requested_by, f.created_at, f.accepted_at,
       p.display_name, p.avatar_upload_id
FROM friendships f
JOIN user_profiles p
  ON p.user_id = (CASE WHEN f.user_low_id = @user_id THEN f.user_high_id ELSE f.user_low_id END)
 AND p.deleted_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = p.user_id AND b.banned_at IS NOT NULL)
WHERE (f.user_low_id = @user_id OR f.user_high_id = @user_id)
  AND f.status = @status
  AND (@direction::text = ''
       OR (@direction::text = 'incoming' AND f.requested_by <> @user_id)
       OR (@direction::text = 'outgoing' AND f.requested_by = @user_id))
  AND p.user_id > @after_user_id
ORDER BY p.user_id
LIMIT @page_size;

-- Son 24 saatte gönderilen arkadaşlık isteği ya da doğrudan davet sayısı
-- (günlük sınır). Satırın sonradan silinmesi sayımı değiştirmez.
-- name: CountRecentEventsByActor :one
SELECT count(*) FROM domain_events
WHERE actor_id = @actor_id AND type = @type AND occurred_at > @since
  AND type IN ('friend.requested', 'tayfa.invited');

-- Kişinin günlük sayımlarını sıraya sokar: aynı kişinin eşzamanlı istekleri
-- sınırı birlikte aşamaz.
-- name: LockUserLimit :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.user_limit:' || sqlc.arg(user_id)::uuid::text, 0));

-- name: IsBlockedEitherWay :one
SELECT EXISTS (
  SELECT 1 FROM user_blocks
  WHERE (blocker_id = @a AND blocked_id = @b) OR (blocker_id = @b AND blocked_id = @a)
);

-- name: CreateBlock :execrows
INSERT INTO user_blocks (blocker_id, blocked_id) VALUES (@blocker_id, @blocked_id)
ON CONFLICT DO NOTHING;

-- name: DeleteBlock :execrows
DELETE FROM user_blocks WHERE blocker_id = @blocker_id AND blocked_id = @blocked_id;

-- Kişinin engelledikleri (yalnızca kendisi görür); engellenen kişinin
-- profili silinmiş olsa da kayıt listelenir ki kaldırılabilsin.
-- name: ListBlocksPage :many
SELECT b.blocked_id, b.created_at, p.display_name, p.avatar_upload_id
FROM user_blocks b
LEFT JOIN user_profiles p ON p.user_id = b.blocked_id AND p.deleted_at IS NULL
WHERE b.blocker_id = @blocker_id AND b.blocked_id > @after_user_id
ORDER BY b.blocked_id
LIMIT @page_size;

-- Profil kopyası olan, silinmemiş ve platform banı olmayan kullanıcı
-- (banlı kullanıcının profili gizlenir, silinmez; issue #104).
-- name: GetLiveUserProfile :one
SELECT up.user_id, up.display_name, up.avatar_upload_id, up.activity_visibility
FROM user_profiles up
WHERE up.user_id = @user_id AND up.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = up.user_id AND b.banned_at IS NOT NULL);

-- Hesap silme (user.deleted): kişinin arkadaşlıkları, istekleri ve iki
-- yöndeki engellemeleri.
-- name: DeleteUserFriendships :exec
DELETE FROM friendships WHERE user_low_id = @user_id OR user_high_id = @user_id;

-- name: DeleteUserBlocks :exec
DELETE FROM user_blocks WHERE blocker_id = @user_id OR blocked_id = @user_id;

-- İki kişi arasındaki istek, kabul ve engelleme sırayla işlenir (satır
-- henüz yokken de).
-- name: LockFriendPair :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.friend_pair:' || sqlc.arg(user_low_id)::uuid::text || ':' || sqlc.arg(user_high_id)::uuid::text, 0));
