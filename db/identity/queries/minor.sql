-- 18 yaş altı sorgusu (ADR-0012, issue #76). Yalnızca kaydı tamamlanmış ve
-- silinmemiş hesapların şifreli doğum tarihi okunur; cevap yalnızca
-- evet/hayır olarak servisten çıkar.

-- name: ListBirthDatesForMinorCheck :many
SELECT id, birth_date_enc
FROM users
WHERE id = ANY(@ids::uuid[])
  AND deleted_at IS NULL
  AND registered_at IS NOT NULL
  AND birth_date_enc IS NOT NULL;
