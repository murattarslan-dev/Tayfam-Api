-- Aynı sürüm ikinci kez onaylanırsa yeni kayıt açılmaz.
-- name: RecordConsent :exec
INSERT INTO consent_records (id, user_id, document_type, document_version)
VALUES (@id, @user_id, @document_type, @document_version)
ON CONFLICT (user_id, document_type, document_version) DO NOTHING;

-- name: HasActiveConsent :one
SELECT EXISTS (
  SELECT 1 FROM consent_records
  WHERE user_id = @user_id AND document_type = @document_type
    AND document_version = @document_version AND withdrawn_at IS NULL
);
