-- Profil tamamlama formu (issue #186). Değerler log'a yazılmaz; yalnızca
-- kişinin kendisine (hesap) ve görünürlüğe göre ProfileService'ten döner.

-- Satır yoksa her alan cevaplanmamış, görünürlük varsayılandır.
-- name: GetOwnProfileDetails :one
SELECT * FROM profile_details WHERE user_id = @user_id;

-- name: UpsertProfileDetails :exec
INSERT INTO profile_details (user_id, interests, about, occupation, team, languages, declined_fields, about_visibility)
VALUES (@user_id, @interests, sqlc.narg(about), sqlc.narg(occupation), sqlc.narg(team), @languages, @declined_fields, @about_visibility)
ON CONFLICT (user_id) DO UPDATE
SET interests = EXCLUDED.interests, about = EXCLUDED.about, occupation = EXCLUDED.occupation,
    team = EXCLUDED.team, languages = EXCLUDED.languages, declined_fields = EXCLUDED.declined_fields,
    about_visibility = EXCLUDED.about_visibility, updated_at = now();

-- "Daha sonra doldur": formu 7 gün göstermez; alan sürümü kaydedilir.
-- name: SnoozeProfilePrompt :exec
INSERT INTO profile_details (user_id, prompt_snoozed_until, prompt_fields_version)
VALUES (@user_id, @until, @fields_version)
ON CONFLICT (user_id) DO UPDATE
SET prompt_snoozed_until = EXCLUDED.prompt_snoozed_until,
    prompt_fields_version = EXCLUDED.prompt_fields_version, updated_at = now();

-- name: ListSocialAccounts :many
SELECT platform, username FROM user_social_accounts WHERE user_id = @user_id ORDER BY platform;

-- name: DeleteSocialAccounts :exec
DELETE FROM user_social_accounts WHERE user_id = @user_id;

-- name: AddSocialAccount :exec
INSERT INTO user_social_accounts (user_id, platform, username) VALUES (@user_id, @platform, @username);

-- Hesap silme: profil alanları hemen gider (kişisel veri).
-- name: DeleteUserProfileDetails :exec
DELETE FROM profile_details WHERE user_id = @user_id;
