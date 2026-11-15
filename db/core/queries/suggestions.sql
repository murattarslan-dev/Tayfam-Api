-- Arkadaşlık puanı ve arkadaş önerisi (issue #208, ADR-0028). Puan hiçbir
-- sorgunun API'ye giden sonucunda yoktur; yalnızca aday sırası için okunur.

-- ---------------------------------------------------------------------------
-- Puan (friend-suggestions işi)

-- name: DeleteAllFriendScores :exec
DELETE FROM friend_scores;

-- Son 12 ayda (@since sonrası, @now öncesi başlayan) silinmemiş ve iptal
-- edilmemiş etkinliklerde ikisi de "attended" olan çiftler. Ağırlık
-- 0,5 ^ (yaş / yarı ömür). Profillemesi kapalı, hesabı silinmiş ya da banlı
-- kullanıcı girmez.
-- name: InsertFriendScores :execrows
WITH att AS (
  SELECT r.user_id, e.id AS event_id, e.starts_at
  FROM event_rsvps r
  JOIN events e ON e.id = r.event_id
  WHERE r.attendance = 'attended'
    AND e.deleted_at IS NULL AND e.status <> 'cancelled'
    AND e.starts_at > @since::timestamptz AND e.starts_at <= @now::timestamptz
    AND NOT EXISTS (SELECT 1 FROM social_settings s WHERE s.user_id = r.user_id AND NOT s.profiling_enabled)
    AND NOT EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = r.user_id AND p.deleted_at IS NOT NULL)
    AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = r.user_id AND b.banned_at IS NOT NULL)
)
INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at)
SELECT a.user_id, b.user_id,
       sum(power(0.5::float8, (date_part('epoch', @now::timestamptz) - date_part('epoch', a.starts_at)) / @half_life_seconds::float8))::float8 AS score,
       count(*)::integer, max(a.starts_at), @now::timestamptz
FROM att a
JOIN att b ON b.event_id = a.event_id AND a.user_id < b.user_id
GROUP BY a.user_id, b.user_id;

-- ---------------------------------------------------------------------------
-- Öneri adayları (friend-suggestions işi)

-- @after_user'dan sonraki alıcılar için, alıcı başına puana göre en çok
-- @per_user aday. Alıcı son 7 günde öneri almışsa hiç aday dönmez.
-- Elenenler (ADR-0028 §3): arkadaşlık ya da bekleyen istek, iki yönde engel,
-- iki yönde şikâyet, reddedilmiş istek, alıcıya daha önce önerilmiş ya da
-- karşı tarafın alıcıyı kapattığı çift, "Beni önerme", silinmiş/banlı aday.
-- name: ListSuggestionCandidates :many
WITH pairs AS (
  SELECT s.user_low_id AS recipient_id, s.user_high_id AS candidate_id, s.score
  FROM friend_scores s WHERE s.shared_count >= @min_shared::integer
  UNION ALL
  SELECT s.user_high_id, s.user_low_id, s.score
  FROM friend_scores s WHERE s.shared_count >= @min_shared::integer
), ranked AS (
  SELECT p.recipient_id, p.candidate_id,
         row_number() OVER (PARTITION BY p.recipient_id ORDER BY p.score DESC, p.candidate_id) AS rn
  FROM pairs p
  WHERE p.recipient_id > @after_user::uuid
    AND NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                    WHERE fs.user_id = p.recipient_id AND fs.source = 'suggested' AND fs.created_at > @week_ago::timestamptz)
    AND NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                    WHERE (fs.user_id = p.recipient_id AND fs.suggested_user_id = p.candidate_id)
                       OR (fs.user_id = p.candidate_id AND fs.suggested_user_id = p.recipient_id AND fs.dismissed_at IS NOT NULL))
    AND NOT EXISTS (SELECT 1 FROM friendships f
                    WHERE f.user_low_id = LEAST(p.recipient_id, p.candidate_id)
                      AND f.user_high_id = GREATEST(p.recipient_id, p.candidate_id))
    AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                    WHERE (ub.blocker_id = p.recipient_id AND ub.blocked_id = p.candidate_id)
                       OR (ub.blocker_id = p.candidate_id AND ub.blocked_id = p.recipient_id))
    AND NOT EXISTS (SELECT 1 FROM reports r
                    WHERE (r.reporter_id = p.recipient_id AND r.subject_user_id = p.candidate_id)
                       OR (r.reporter_id = p.candidate_id AND r.subject_user_id = p.recipient_id))
    AND NOT EXISTS (SELECT 1 FROM friend_request_outcomes o
                    WHERE o.outcome = 'declined'
                      AND ((o.requester_id = p.recipient_id AND o.recipient_id = p.candidate_id)
                        OR (o.requester_id = p.candidate_id AND o.recipient_id = p.recipient_id)))
    AND NOT EXISTS (SELECT 1 FROM social_settings ss WHERE ss.user_id = p.candidate_id AND ss.hide_from_suggestions)
    AND NOT EXISTS (SELECT 1 FROM user_profiles up WHERE up.user_id IN (p.recipient_id, p.candidate_id) AND up.deleted_at IS NOT NULL)
    AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id IN (p.recipient_id, p.candidate_id) AND b.banned_at IS NOT NULL)
)
SELECT recipient_id::uuid AS recipient_id, candidate_id::uuid AS candidate_id
FROM ranked
WHERE rn <= @per_user::integer
ORDER BY recipient_id, rn
LIMIT @n;

-- Aynı alıcının eşzamanlı önerileri sıraya girer.
-- name: LockSuggestionRecipient :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.friend_suggestion:' || sqlc.arg(user_id)::uuid::text, 0));

-- Öneriyi yazar; transaction içinde haftalık sınırı, arkadaşlığı, engeli,
-- "Beni önerme"yi, kapatmayı ve profillemeyi yeniden denetler. Yazılmadıysa
-- 0 döner.
-- name: CreateFriendSuggestion :execrows
INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at)
SELECT @user_id::uuid, @suggested_user_id::uuid, @now::timestamptz
WHERE NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                  WHERE fs.user_id = @user_id::uuid AND fs.source = 'suggested' AND fs.created_at > @week_ago::timestamptz)
  AND NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                  WHERE fs.user_id = @suggested_user_id::uuid AND fs.suggested_user_id = @user_id::uuid
                    AND fs.dismissed_at IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM friendships f
                  WHERE f.user_low_id = LEAST(@user_id::uuid, @suggested_user_id::uuid)
                    AND f.user_high_id = GREATEST(@user_id::uuid, @suggested_user_id::uuid))
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = @user_id::uuid AND ub.blocked_id = @suggested_user_id::uuid)
                     OR (ub.blocker_id = @suggested_user_id::uuid AND ub.blocked_id = @user_id::uuid))
  AND NOT EXISTS (SELECT 1 FROM social_settings ss
                  WHERE (ss.user_id = @suggested_user_id::uuid AND (ss.hide_from_suggestions OR NOT ss.profiling_enabled))
                     OR (ss.user_id = @user_id::uuid AND NOT ss.profiling_enabled))
ON CONFLICT (user_id, suggested_user_id) DO NOTHING;

-- Saklama: 365 günden eski açık öneriler (kapatılmışlar kalır).
-- name: DeleteOldOpenFriendSuggestions :execrows
DELETE FROM friend_suggestions
WHERE dismissed_at IS NULL AND created_at < @before::timestamptz;

-- ---------------------------------------------------------------------------
-- Öneri listesi ve kapatma (kullanıcının kendi token'ıyla)

-- Alıcının @since sonrasındaki açık önerileri; her okumada yeniden süzülür.
-- Puan ve ortak katılım sayısı okunmaz.
-- name: ListOpenFriendSuggestions :many
SELECT fs.suggested_user_id, fs.created_at, p.display_name, p.avatar_upload_id
FROM friend_suggestions fs
JOIN user_profiles p ON p.user_id = fs.suggested_user_id AND p.deleted_at IS NULL
WHERE fs.user_id = @user_id AND fs.dismissed_at IS NULL AND fs.created_at > @since::timestamptz
  AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = fs.suggested_user_id AND b.banned_at IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM friendships f
                  WHERE f.user_low_id = LEAST(fs.user_id, fs.suggested_user_id)
                    AND f.user_high_id = GREATEST(fs.user_id, fs.suggested_user_id))
  AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = fs.user_id AND ub.blocked_id = fs.suggested_user_id)
                     OR (ub.blocker_id = fs.suggested_user_id AND ub.blocked_id = fs.user_id))
  AND NOT EXISTS (SELECT 1 FROM reports r
                  WHERE (r.reporter_id = fs.user_id AND r.subject_user_id = fs.suggested_user_id)
                     OR (r.reporter_id = fs.suggested_user_id AND r.subject_user_id = fs.user_id))
  AND NOT EXISTS (SELECT 1 FROM social_settings ss
                  WHERE (ss.user_id = fs.suggested_user_id AND (ss.hide_from_suggestions OR NOT ss.profiling_enabled))
                     OR (ss.user_id = fs.user_id AND NOT ss.profiling_enabled))
ORDER BY fs.created_at DESC, fs.suggested_user_id
LIMIT @n;

-- name: DismissFriendSuggestion :execrows
UPDATE friend_suggestions SET dismissed_at = @now::timestamptz
WHERE user_id = @user_id AND suggested_user_id = @suggested_user_id AND dismissed_at IS NULL;

-- ---------------------------------------------------------------------------
-- Ayarlar

-- name: GetSocialSettings :one
SELECT profiling_enabled, hide_from_suggestions FROM social_settings WHERE user_id = @user_id;

-- Verilmeyen alan (NULL) olduğu gibi kalır; satır yoksa varsayılanlardan
-- açılır.
-- name: UpsertSocialSettings :one
INSERT INTO social_settings (user_id, profiling_enabled, hide_from_suggestions, updated_at)
VALUES (@user_id, COALESCE(sqlc.narg(profiling_enabled)::boolean, true),
        COALESCE(sqlc.narg(hide_from_suggestions)::boolean, false), @now::timestamptz)
ON CONFLICT (user_id) DO UPDATE SET
  profiling_enabled     = COALESCE(sqlc.narg(profiling_enabled)::boolean, social_settings.profiling_enabled),
  hide_from_suggestions = COALESCE(sqlc.narg(hide_from_suggestions)::boolean, social_settings.hide_from_suggestions),
  updated_at            = @now::timestamptz
RETURNING profiling_enabled, hide_from_suggestions;

-- Profilleme kapatma ve hesap silme: kişinin bütün puanları.
-- name: DeleteUserFriendScores :exec
DELETE FROM friend_scores WHERE user_low_id = @user_id OR user_high_id = @user_id;

-- Profilleme kapatma: kişiye yapılmış ve kişinin önerildiği açık öneriler.
-- Kapatılmış satırlar kalır.
-- name: DeleteOpenSuggestionsInvolving :exec
DELETE FROM friend_suggestions
WHERE (user_id = @user_id OR suggested_user_id = @user_id) AND dismissed_at IS NULL;

-- "Beni önerme": kişinin önerildiği açık öneriler.
-- name: DeleteOpenSuggestionsOf :exec
DELETE FROM friend_suggestions WHERE suggested_user_id = @user_id AND dismissed_at IS NULL;

-- ---------------------------------------------------------------------------
-- Engelleme ve hesap silme

-- name: DeleteFriendScore :exec
DELETE FROM friend_scores WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id;

-- Engelleme: iki yöndeki açık öneriler silinir, engelleyen adına kapatılmış
-- kayıt yazılır (çift bir daha önerilmez).
-- name: DeleteOpenSuggestionsBetween :exec
DELETE FROM friend_suggestions
WHERE dismissed_at IS NULL
  AND ((user_id = @a AND suggested_user_id = @b) OR (user_id = @b AND suggested_user_id = @a));

-- name: DismissSuggestionPair :exec
INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at, source)
VALUES (@user_id, @suggested_user_id, @now::timestamptz, @now::timestamptz, 'blocked')
ON CONFLICT (user_id, suggested_user_id) DO UPDATE SET dismissed_at = EXCLUDED.dismissed_at
WHERE friend_suggestions.dismissed_at IS NULL;

-- name: DeleteUserFriendSuggestions :exec
DELETE FROM friend_suggestions WHERE user_id = @user_id OR suggested_user_id = @user_id;

-- name: DeleteUserSocialSettings :exec
DELETE FROM social_settings WHERE user_id = @user_id;
