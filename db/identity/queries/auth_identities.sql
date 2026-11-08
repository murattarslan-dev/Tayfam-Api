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
