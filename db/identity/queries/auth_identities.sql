-- name: GetAuthIdentity :one
SELECT id, user_id, provider, subject, email
FROM auth_identities
WHERE provider = @provider AND subject = @subject;

-- name: CreateAuthIdentity :exec
INSERT INTO auth_identities (id, user_id, provider, subject, email)
VALUES (@id, @user_id, @provider, @subject, @email);

-- name: TouchAuthIdentity :exec
UPDATE auth_identities
SET last_used_at = now(), email = @email
WHERE id = @id;

-- Hesap silmede giriş kimlikleri hemen silinir; aynı Google/Apple hesabıyla
-- sonraki giriş yeni bir hesap açar (veri modeli v14).
-- name: DeleteUserAuthIdentities :exec
DELETE FROM auth_identities WHERE user_id = @user_id;

-- Hesabın Apple kimliği (ADR-0045 §4): hesap silmeden önce token iptali için
-- gereken Apple kullanıcı kimliği. Apple kimliği olmayan hesapta satır yoktur.
-- name: GetUserAppleSubject :one
SELECT subject
FROM auth_identities
WHERE user_id = @user_id AND provider = 'apple'
ORDER BY id
LIMIT 1;
