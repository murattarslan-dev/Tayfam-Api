-- Profil kopyası (ADR-0009). Yalnızca UserEventService yazar.

-- Daha yeni bir olay uygulanmışsa ya da profil silinmişse değişmez; etkilenen
-- satır 0 olur.
-- activity_visibility NULL ise (alanı taşımayan eski olay) mevcut değer
-- kalır; yeni satırda varsayılan 'tayfa' olur.
-- name: UpsertUserProfile :execrows
INSERT INTO user_profiles (user_id, display_name, avatar_upload_id, source_event_id, activity_visibility)
VALUES (@user_id, @display_name, @avatar_upload_id, @source_event_id, COALESCE(sqlc.narg(activity_visibility)::text, 'tayfa'))
ON CONFLICT (user_id) DO UPDATE
SET display_name = EXCLUDED.display_name,
    avatar_upload_id = EXCLUDED.avatar_upload_id,
    activity_visibility = COALESCE(sqlc.narg(activity_visibility)::text, user_profiles.activity_visibility),
    source_event_id = EXCLUDED.source_event_id,
    updated_at = now()
WHERE user_profiles.deleted_at IS NULL
  AND user_profiles.source_event_id < EXCLUDED.source_event_id;

-- Hesap silindi: ad ve avatar temizlenir, satır mezar taşı olarak kalır.
-- Tekrar uygulanınca sonuç değişmez.
-- name: TombstoneUserProfile :exec
INSERT INTO user_profiles (user_id, source_event_id, deleted_at)
VALUES (@user_id, @source_event_id, now())
ON CONFLICT (user_id) DO UPDATE
SET display_name = NULL,
    avatar_upload_id = NULL,
    deleted_at = COALESCE(user_profiles.deleted_at, now()),
    source_event_id = GREATEST(user_profiles.source_event_id, EXCLUDED.source_event_id),
    updated_at = now();

-- name: GetUserProfile :one
SELECT user_id, display_name, avatar_upload_id, source_event_id, deleted_at
FROM user_profiles
WHERE user_id = @user_id;
