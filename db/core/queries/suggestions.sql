-- Arkadaşlık puanı ve arkadaş önerisi (issue #208, ADR-0030). Puan hiçbir
-- sorgunun API'ye giden sonucunda yoktur; yalnızca aday sırası için okunur.
-- Bir çiftin önerilebilmesi friend_suggestion_allowed (migration 00025)
-- ile tek yerde tanımlıdır.

-- ---------------------------------------------------------------------------
-- Puan (friend-suggestions işi)

-- Puan hesaplaması ile profilleme kapatma ve engelleme sıraya girer: iş
-- özel, ayar ve engel paylaşımlı kilit alır. Böylece kapatma, hesaplanmakta
-- olan satırları kaçırmaz.
-- name: LockFriendScores :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.friend_scores', 0));

-- name: LockFriendScoresShared :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('tayfam.core.friend_scores', 0));

-- name: DeleteAllFriendScores :exec
DELETE FROM friend_scores;

-- Son 12 ayda (since sonrası, now öncesi başlayan) silinmemiş ve iptal
-- edilmemiş etkinliklerde ikisi de "attended" olan çiftler. Ağırlık
-- 0,5 ^ (yaş / yarı ömür). Profillemesi kapalı, hesabı silinmiş ya da banlı
-- kullanıcı ve aralarında engel olan çift girmez.
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
WHERE NOT EXISTS (SELECT 1 FROM user_blocks ub
                  WHERE (ub.blocker_id = a.user_id AND ub.blocked_id = b.user_id)
                     OR (ub.blocker_id = b.user_id AND ub.blocked_id = a.user_id))
GROUP BY a.user_id, b.user_id;

-- Aynı transaction'da, yazılan puanlardan profillemesi kapalı olanlarınki
-- ve engelli çiftler silinir (hesaplama sırasında değişen ayar ve engel).
-- name: DeleteExcludedFriendScores :execrows
DELETE FROM friend_scores fs
WHERE EXISTS (SELECT 1 FROM social_settings s
              WHERE s.user_id IN (fs.user_low_id, fs.user_high_id) AND NOT s.profiling_enabled)
   OR EXISTS (SELECT 1 FROM user_blocks ub
              WHERE (ub.blocker_id = fs.user_low_id AND ub.blocked_id = fs.user_high_id)
                 OR (ub.blocker_id = fs.user_high_id AND ub.blocked_id = fs.user_low_id));

-- ---------------------------------------------------------------------------
-- Öneri adayları (friend-suggestions işi)

-- after_user'dan sonraki alıcılar için, alıcı başına puana göre en çok
-- per_user aday. Alıcı son 7 günde öneri almışsa hiç aday dönmez; alıcıya
-- daha önce önerilmiş kişi aday olmaz.
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
                    WHERE fs.user_id = p.recipient_id AND fs.suggested_user_id = p.candidate_id)
    AND friend_suggestion_allowed(p.recipient_id, p.candidate_id)
)
SELECT recipient_id::uuid AS recipient_id, candidate_id::uuid AS candidate_id
FROM ranked
WHERE rn <= @per_user::integer
ORDER BY recipient_id, rn
LIMIT @n;

-- Aynı alıcının eşzamanlı önerileri sıraya girer.
-- name: LockSuggestionRecipient :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.friend_suggestion:' || sqlc.arg(user_id)::uuid::text, 0));

-- Öneriyi yazar; transaction içinde aday sorgusunun bütün koşullarını
-- (haftalık sınır, daha önce önerilmiş, friend_suggestion_allowed) yeniden
-- denetler. Yazılmadıysa 0 döner.
-- name: CreateFriendSuggestion :execrows
INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, source)
SELECT @user_id::uuid, @suggested_user_id::uuid, @now::timestamptz, 'suggested'
WHERE NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                  WHERE fs.user_id = @user_id::uuid AND fs.source = 'suggested' AND fs.created_at > @week_ago::timestamptz)
  AND friend_suggestion_allowed(@user_id::uuid, @suggested_user_id::uuid)
ON CONFLICT (user_id, suggested_user_id) DO NOTHING;

-- Saklama: 365 günden eski kapatılmamış satırlar (kapatılmışlar kalır).
-- name: DeleteOldOpenFriendSuggestions :execrows
DELETE FROM friend_suggestions
WHERE dismissed_at IS NULL AND created_at < @before::timestamptz;

-- ---------------------------------------------------------------------------
-- Öneri listesi, kapatma ve bildirim durumu (kullanıcının kendi token'ıyla)

-- Alıcının since sonrasındaki gösterilebilir önerileri; her okumada
-- friend_suggestion_allowed ile yeniden süzülür. Puan ve ortak katılım
-- sayısı okunmaz.
-- name: ListOpenFriendSuggestions :many
SELECT fs.suggested_user_id, fs.created_at, p.display_name, p.avatar_upload_id
FROM friend_suggestions fs
JOIN user_profiles p ON p.user_id = fs.suggested_user_id AND p.deleted_at IS NULL
WHERE fs.user_id = @user_id AND fs.source = 'suggested'
  AND fs.dismissed_at IS NULL AND fs.withdrawn_at IS NULL
  AND fs.created_at > @since::timestamptz
  AND friend_suggestion_allowed(fs.user_id, fs.suggested_user_id)
ORDER BY fs.created_at DESC, fs.suggested_user_id
LIMIT @n;

-- Bildirim listesindeki friend_suggestion bildirimlerinin durumu (chat
-- GetRequestResolutions ile sorar): alıcının o kişiyle önerisi, gösterilip
-- gösterilemeyeceği ve aralarındaki arkadaşlık satırı.
-- name: FriendSuggestionStates :many
SELECT u.suggested_id::uuid AS suggested_id,
       fs.created_at, fs.dismissed_at, fs.withdrawn_at, fs.source,
       (fs.user_id IS NOT NULL AND friend_suggestion_allowed(@user_id::uuid, u.suggested_id)
        AND EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = u.suggested_id AND p.deleted_at IS NULL))::boolean AS allowed,
       (f.user_low_id IS NOT NULL)::boolean AS has_friendship
FROM unnest(@suggested_ids::uuid[]) AS u(suggested_id)
LEFT JOIN friend_suggestions fs ON fs.user_id = @user_id::uuid AND fs.suggested_user_id = u.suggested_id
LEFT JOIN friendships f
  ON f.user_low_id = LEAST(u.suggested_id, @user_id::uuid) AND f.user_high_id = GREATEST(u.suggested_id, @user_id::uuid);

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

-- Profilleme kapatma: kişiye yapılmış ve kişinin önerildiği açık öneriler
-- geri çekilir. Satırlar kalır: haftalık sınır ve "önerildi" hafızası
-- ayarı açıp kapatmakla sıfırlanmaz.
-- name: WithdrawSuggestionsInvolving :exec
UPDATE friend_suggestions SET withdrawn_at = @now::timestamptz
WHERE (user_id = @user_id OR suggested_user_id = @user_id)
  AND dismissed_at IS NULL AND withdrawn_at IS NULL;

-- "Beni önerme": kişinin önerildiği açık öneriler geri çekilir.
-- name: WithdrawSuggestionsOf :exec
UPDATE friend_suggestions SET withdrawn_at = @now::timestamptz
WHERE suggested_user_id = @user_id AND dismissed_at IS NULL AND withdrawn_at IS NULL;

-- ---------------------------------------------------------------------------
-- Kalıcı dışlama (engelleme, isteği reddetme, arkadaşlığı bitirme) ve
-- hesap silme

-- name: DeleteFriendScore :exec
DELETE FROM friend_scores WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id;

-- Karşı tarafın actor'a yapılmış açık önerisi geri çekilir.
-- name: WithdrawSuggestionTo :exec
UPDATE friend_suggestions SET withdrawn_at = @now::timestamptz
WHERE user_id = @user_id AND suggested_user_id = @suggested_user_id
  AND dismissed_at IS NULL AND withdrawn_at IS NULL;

-- actor adına kalıcı kapatma: actor'a yapılmış öneri varsa kapatılır, yoksa
-- source ile baştan kapatılmış kayıt yazılır. Çift bir daha hiçbir yönde
-- önerilmez.
-- name: DismissSuggestionPair :exec
INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at, source)
VALUES (@user_id, @suggested_user_id, @now::timestamptz, @now::timestamptz, @source::text)
ON CONFLICT (user_id, suggested_user_id) DO UPDATE SET dismissed_at = EXCLUDED.dismissed_at
WHERE friend_suggestions.dismissed_at IS NULL;

-- name: DeleteUserFriendSuggestions :exec
DELETE FROM friend_suggestions WHERE user_id = @user_id OR suggested_user_id = @user_id;

-- name: DeleteUserSocialSettings :exec
DELETE FROM social_settings WHERE user_id = @user_id;
