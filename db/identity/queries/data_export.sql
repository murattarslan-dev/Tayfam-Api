-- KVKK veri dışa aktarma talepleri (issue #144, ADR-0021). Kullanıcı
-- uçlarındaki her okuma ve yazma çağıranın user_id'siyle sınırlıdır:
-- başkasının talebi "yok" sayılır (IDOR). wrapped_key yalnızca indirme
-- anında okunur; hiçbir log'a ve yanıta açık haliyle girmez.

-- name: CreateDataExportRequest :one
INSERT INTO data_export_requests (id, user_id, status, upload_id, client_request_id, requested_at)
VALUES (@id, @user_id, 'pending', @upload_id, @client_request_id, @now)
ON CONFLICT (user_id, client_request_id) DO NOTHING
RETURNING *;

-- name: GetDataExportByClientRequest :one
SELECT * FROM data_export_requests
WHERE user_id = @user_id AND client_request_id = @client_request_id;

-- name: GetOwnDataExport :one
SELECT * FROM data_export_requests
WHERE id = @id AND user_id = @user_id;

-- Son 30 günün talepleri, yeni önce.
-- name: ListOwnDataExports :many
SELECT * FROM data_export_requests
WHERE user_id = @user_id AND requested_at > @since
ORDER BY requested_at DESC, id DESC
LIMIT 50;

-- Etkin (bekleyen ya da hazırlanan) talep.
-- name: HasActiveDataExport :one
SELECT EXISTS (
  SELECT 1 FROM data_export_requests
  WHERE user_id = @user_id AND status IN ('pending', 'processing')
)::boolean;

-- 30 günlük sınıra sayılan son talep; başarısız talepler sayılmaz.
-- name: LastCountedDataExport :one
SELECT max(requested_at)::timestamptz FROM data_export_requests
WHERE user_id = @user_id AND status <> 'failed';

-- name: MarkDataExportProcessing :execrows
UPDATE data_export_requests SET status = 'processing', started_at = @now
WHERE id = @id AND status = 'pending';

-- Hazır: hesap silinmemişse ve talep hâlâ hazırlanıyorsa.
-- name: MarkDataExportReady :execrows
UPDATE data_export_requests r
SET status = 'ready', wrapped_key = @wrapped_key, size_bytes = @size_bytes,
    ready_at = @now, expires_at = @expires_at, finished_at = @now
WHERE r.id = @id AND r.status = 'processing'
  AND EXISTS (SELECT 1 FROM users u WHERE u.id = r.user_id AND u.deleted_at IS NULL);

-- name: MarkDataExportFailed :execrows
UPDATE data_export_requests
SET status = 'failed', failure_reason = @failure_reason, finished_at = @now
WHERE id = @id AND status IN ('pending', 'processing');

-- İlk indirme zamanı kalır.
-- name: MarkDataExportDownloaded :exec
UPDATE data_export_requests SET downloaded_at = COALESCE(downloaded_at, @now)
WHERE id = @id AND user_id = @user_id AND status = 'ready';

-- Hesap silme: talepler ve anahtarları hemen silinir.
-- name: DeleteUserDataExports :exec
DELETE FROM data_export_requests WHERE user_id = @user_id;

-- Saklama işi (ADR-0021): 7 günü dolan hazır talebin anahtarı ve dosya
-- kaydı silinir (kripto-silme; dosyayı core siler).
-- name: ExpireDataExports :execrows
UPDATE data_export_requests
SET status = 'expired', wrapped_key = NULL, upload_id = NULL, size_bytes = NULL
WHERE id IN (
  SELECT x.id FROM data_export_requests x
  WHERE x.status = 'ready' AND x.expires_at < @cutoff
  LIMIT @batch
) AND status = 'ready' AND expires_at < @cutoff;

-- Yarıda kalan talepler (identity yeniden başladı, token süresi doldu)
-- başarısız sayılır; 30 günlük sınırı yemez.
-- name: FailStaleDataExports :execrows
UPDATE data_export_requests
SET status = 'failed', failure_reason = 'timeout', finished_at = now()
WHERE id IN (
  SELECT x.id FROM data_export_requests x
  WHERE x.status IN ('pending', 'processing') AND x.requested_at < @cutoff
  LIMIT @batch
) AND status IN ('pending', 'processing') AND requested_at < @cutoff;

-- Satır 30 gün tutulur ("30 günde bir" sınırı); anahtarsız ve dosyasızdır.
-- name: PurgeDataExports :execrows
DELETE FROM data_export_requests
WHERE id IN (
  SELECT x.id FROM data_export_requests x
  WHERE x.status IN ('expired', 'failed') AND x.requested_at < @cutoff
  LIMIT @batch
);

-- Dışa aktarmanın identity bölümleri (docs/data-export-format.md). Doğum
-- tarihi şifreli okunur ve uygulamada çözülür.
-- name: ExportUserProfile :one
SELECT id, display_name, avatar_upload_id, locale, birth_date_enc, birth_date_visibility, birth_date_audience,
       activity_visibility, ref_code, referred_by_user_id, referred_at, registered_at, created_at
FROM users
WHERE id = @id AND deleted_at IS NULL;

-- name: ExportAuthIdentities :many
SELECT provider, email, created_at, last_used_at FROM auth_identities
WHERE user_id = @user_id ORDER BY created_at, id;

-- name: ExportDevices :many
SELECT id, platform, created_at, last_seen_at FROM devices
WHERE user_id = @user_id ORDER BY created_at, id;

-- name: ExportConsents :many
SELECT document_type, document_version, accepted_at, withdrawn_at FROM consent_records
WHERE user_id = @user_id ORDER BY accepted_at, id;
