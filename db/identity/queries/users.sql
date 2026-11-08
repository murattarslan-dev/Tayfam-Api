-- name: CreateUser :exec
INSERT INTO users (id, display_name, locale)
VALUES (@id, @display_name, @locale);

-- name: GetUser :one
SELECT id, display_name, avatar_upload_id, locale, birth_date_enc,
       birth_date_visibility, birth_date_audience, activity_visibility,
       ref_code, registered_at, age_blocked_at, created_at
FROM users
WHERE id = @id AND deleted_at IS NULL;

-- Kayıt bir kez tamamlanır; yaş nedeniyle reddedilmiş veya silinmiş hesapta
-- tamamlanamaz. Etkilenen satır sayısı 0 ise kayıt zaten yapılmış ya da
-- engellenmiştir.
-- name: CompleteRegistration :execrows
UPDATE users
SET birth_date_enc = @birth_date_enc, registered_at = now()
WHERE id = @id
  AND registered_at IS NULL
  AND age_blocked_at IS NULL
  AND deleted_at IS NULL;

-- name: BlockRegistrationForAge :execrows
UPDATE users
SET age_blocked_at = now()
WHERE id = @id
  AND registered_at IS NULL
  AND age_blocked_at IS NULL
  AND deleted_at IS NULL;

-- name: SetRefCode :execrows
UPDATE users SET ref_code = @ref_code, ref_code_rotated_at = now()
WHERE id = @id AND ref_code IS NULL;

-- name: SetDisplayName :exec
UPDATE users SET display_name = @display_name WHERE id = @id AND deleted_at IS NULL;

-- Yaş nedeniyle reddedilen hesapta e-posta tutulmaz (veri azaltma).
-- name: ClearIdentityEmails :exec
UPDATE auth_identities SET email = NULL WHERE user_id = @user_id;
