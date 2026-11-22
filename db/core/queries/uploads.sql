-- Yüklenen dosyalar (issue #135; ADR-0007, ADR-0020; internal/core/app/media).
-- r2_key hiçbir yanıta, log'a ve olaya girmez; yalnızca R2 istemcisine
-- verilir.

-- name: CreateUpload :one
INSERT INTO uploads (id, user_id, tayfa_id, purpose, r2_key, preview_type, preview_bytes, full_type, full_bytes, client_request_id)
VALUES (@id, @user_id, sqlc.narg(tayfa_id), @purpose, @r2_key, sqlc.narg(preview_type)::text, sqlc.narg(preview_bytes)::bigint, @full_type::text, @full_bytes::bigint, @client_request_id)
ON CONFLICT (user_id, client_request_id) DO NOTHING
RETURNING id;

-- name: GetUploadByClientRequest :one
SELECT * FROM uploads
WHERE user_id = @user_id AND client_request_id = @client_request_id AND purpose <> 'data_export';

-- Sahibinin görüntüsü; başkasınınki bulunamaz (IDOR). Dışa aktarma
-- dosyaları (data_export) görüntü yollarında hiç görünmez.
-- name: GetOwnUpload :one
SELECT * FROM uploads
WHERE id = @id AND user_id = @user_id AND status <> 'deleting' AND purpose <> 'data_export';

-- name: GetOwnUploadForUpdate :one
SELECT * FROM uploads
WHERE id = @id AND user_id = @user_id AND status <> 'deleting' AND purpose <> 'data_export'
FOR UPDATE;

-- Kullanıcının yükleme açmalarını sıraya koyar: sıklık sınırı ve kota
-- kontrolü ile kaydın yazılması arasında eşzamanlı ikinci bir istek
-- araya giremez. Transaction bitince bırakılır.
-- name: LockUserUploads :exec
SELECT pg_advisory_xact_lock(hashtextextended('uploads:' || @user_id::text, 0));

-- Saatlik sıklık sınırı: son bir saatte açılan kayıtlar (silinenler
-- sayılmaz; pending kayıtlar 24 saat durduğu için yeterli bir alt sınırdır).
-- name: CountUploadsSince :one
SELECT count(*) FROM uploads
WHERE user_id = @user_id AND created_at > @since AND purpose <> 'data_export';

-- Kota: iki varyantın (birebir ekte tek nesnenin) bildirilen (ve imzayla
-- zorlanan) boyutları.
-- name: UserStoredBytes :one
SELECT COALESCE(sum(COALESCE(preview_bytes, 0) + full_bytes), 0)::bigint FROM uploads
WHERE user_id = @user_id AND status NOT IN ('rejected', 'deleting') AND purpose <> 'data_export';

-- Denetimden geçti (ADR-0020). Yalnızca bekleyen kayıt; eşzamanlı ikinci
-- tamamlama bir şey değiştirmez.
-- name: MarkUploadReady :execrows
UPDATE uploads SET status = 'ready', width = @width, height = @height, ready_at = @now
WHERE id = @id AND status = 'pending';

-- name: RejectPendingUpload :execrows
UPDATE uploads SET status = 'rejected', reject_reason = @reject_reason
WHERE id = @id AND status = 'pending';

-- Bağlama: yalnızca sahibinin, hazır, amacı ve tayfası tutan, başka bir
-- içeriğe bağlanmamış dosyası. Aynı hedefe yeniden bağlama bir şey
-- değiştirmez. Çağıran satırı önce GetOwnUploadForUpdate ile kilitler ve
-- hatayı ayırt eder; bu koşul son savunma hattıdır.
-- name: AttachUpload :execrows
UPDATE uploads SET attached_type = @attached_type::text, attached_id = @attached_id::uuid,
    attached_at = COALESCE(attached_at, @now)
WHERE id = @id AND user_id = @user_id AND purpose = @purpose AND status = 'ready'
  AND tayfa_id IS NOT DISTINCT FROM sqlc.narg(tayfa_id)::uuid
  AND (attached_id IS NULL OR (attached_type = @attached_type::text AND attached_id = @attached_id::uuid));

-- Kullanıcının güncel avatarı dışındaki bağlı avatarları silinir
-- (identity'nin user.profile_updated olayı). keep NULL: avatar kaldırıldı.
-- name: ReleaseOtherAvatars :execrows
UPDATE uploads SET status = 'deleting'
WHERE user_id = @user_id AND purpose = 'avatar' AND attached_type = 'user' AND attached_id = @user_id
  AND status = 'ready'
  AND (sqlc.narg(keep)::uuid IS NULL OR id <> sqlc.narg(keep)::uuid);

-- Hesap silme (user.deleted): kullanıcının dosyaları silinir; bağlı kapak
-- görselleri tayfanındır, bağlı şikâyet kanıtı platformundur (rapor
-- durdukça, ADR-0031); yalnızca sahiplikleri kaldırılır.
-- name: DeleteUserUploads :execrows
UPDATE uploads SET status = 'deleting'
WHERE user_id = @user_id AND status <> 'deleting'
  AND NOT (purpose IN ('cover', 'report_evidence') AND attached_type IS NOT NULL);

-- name: DisownUserCovers :execrows
UPDATE uploads SET user_id = NULL
WHERE user_id = @user_id AND purpose IN ('cover', 'report_evidence') AND attached_type IS NOT NULL;

-- Sohbet mesajı fotoğrafını ya da ekini bıraktı (chat.media_released,
-- ADR-0031): dosya hâlâ o mesaja bağlıysa silinir; değilse bir şey olmaz
-- (tekrar gelen olay).
-- name: ReleaseMessageMedia :execrows
UPDATE uploads SET status = 'deleting'
WHERE id = @id AND attached_type IN ('channel_message', 'dm_message') AND attached_id = @message_id
  AND status <> 'deleting';

-- Onaylanmamış sohbet bağlaması (ADR-0031): chat'in henüz onaylamadığı,
-- belki hiç yazılmamış bir mesaja bağlı dosya, aynı sahibin yeni mesajına
-- taşınır. Çağıran satırı önce GetOwnUploadForUpdate ile kilitler.
-- name: MoveChatAttachment :execrows
UPDATE uploads SET attached_id = @attached_id::uuid, attached_at = @now
WHERE id = @id AND user_id = @user_id AND purpose = @purpose AND status = 'ready'
  AND tayfa_id IS NOT DISTINCT FROM sqlc.narg(tayfa_id)::uuid
  AND attached_type = @attached_type::text AND attach_confirmed_at IS NULL;

-- Uzlaştırma işi: onaylanmamış, before'dan eski sohbet bağlamaları.
-- name: ListUnconfirmedChatAttachments :many
SELECT id, attached_type::text AS attached_type, attached_id::uuid AS attached_id FROM uploads
WHERE attached_type IN ('channel_message', 'dm_message') AND attach_confirmed_at IS NULL
  AND status = 'ready' AND attached_at < @before
ORDER BY attached_at
LIMIT @n;

-- chat dosyayı tutan mesajı söyledi: bağlama o mesajla onaylanır. Arada
-- taşındıysa (asked_id değişti) bir şey olmaz.
-- name: ConfirmChatAttachment :execrows
UPDATE uploads SET attached_id = @holder_id::uuid, attach_confirmed_at = @now
WHERE id = @id AND attached_type = @attached_type::text AND attached_id = @asked_id
  AND attach_confirmed_at IS NULL AND status = 'ready';

-- Hiçbir mesaj dosyayı tutmuyor: bağlama kaldırılır; dosya bağlanmamış
-- hazır dosya olarak 7 günlük temizliğe döner (ready_at'ten sayılır).
-- name: DetachChatAttachment :execrows
UPDATE uploads SET attached_type = NULL, attached_id = NULL, attached_at = NULL
WHERE id = @id AND attached_type = @attached_type::text AND attached_id = @asked_id
  AND attach_confirmed_at IS NULL AND status = 'ready';

-- Tayfa kalıcı silindi: kanal fotoğrafları da gider (kanallar chat'te
-- tayfa.purged ile silinir; dosyanın tayfası core'da bellidir).
-- name: ReleaseTayfaChatPhotos :execrows
UPDATE uploads SET status = 'deleting'
WHERE tayfa_id = @tayfa_id AND purpose = 'chat_photo' AND status <> 'deleting';

-- Platform panelinde şikâyetin kanıt fotoğrafı (issue #205): yalnızca o
-- rapora bağlı, hazır dosya.
-- name: GetReportEvidenceUpload :one
SELECT r2_key, full_type::text AS full_type FROM uploads
WHERE id = @id AND purpose = 'report_evidence' AND status = 'ready'
  AND attached_type = 'report' AND attached_id = @report_id;

-- Temizlik: 24 saatte tamamlanmayan, 24 saattir reddedilmiş ve 7 gün
-- içinde bağlanmamış görüntüler ile süresi dolan dışa aktarmalar silme
-- kuyruğuna girer.
-- name: ExpireUploads :many
UPDATE uploads SET status = 'deleting'
WHERE id IN (
    SELECT q.id FROM uploads q
    WHERE (q.status = 'pending' AND q.created_at < @pending_before)
       OR (q.status = 'rejected' AND q.updated_at < @rejected_before)
       OR (q.status = 'ready' AND q.purpose <> 'data_export' AND q.attached_type IS NULL AND q.ready_at < @unattached_before)
       OR (q.status = 'ready' AND q.purpose = 'data_export' AND q.expires_at < @now)
    ORDER BY q.id
    LIMIT @n
    FOR UPDATE SKIP LOCKED
  )
  AND (
       (status = 'pending' AND created_at < @pending_before)
    OR (status = 'rejected' AND updated_at < @rejected_before)
    OR (status = 'ready' AND purpose <> 'data_export' AND attached_type IS NULL AND ready_at < @unattached_before)
    OR (status = 'ready' AND purpose = 'data_export' AND expires_at < @now)
  )
RETURNING id;

-- name: ListDeletingUploads :many
SELECT id, r2_key, purpose FROM uploads
WHERE status = 'deleting'
ORDER BY updated_at, id
LIMIT @n;

-- R2'deki nesneleri silindikten sonra. İçerikteki referans ON DELETE SET
-- NULL ile düşer.
-- name: DeleteUploadRow :execrows
DELETE FROM uploads WHERE id = @id AND status = 'deleting';

-- Görüntüleme: istenen hazır dosyalar ve izleyicinin her birini görüp
-- göremeyeceğine karar vermek için bağlam. Karar uygulama katmanında, core'un
-- izin tablosuyla verilir (authz); burada yalnızca veri toplanır. İçeriğin
-- tayfası: tayfa kapağında tayfanın kendisi, etkinlik kapağında ve av
-- fotoğrafında ve albüm fotoğrafında etkinliğin tayfası, soru havuzunun
-- sorusunda ve cevabında sorunun tayfası (silinmiş içerik ve tayfa bağlam
-- vermez).
-- name: ListMediaForViewer :many
SELECT u.id, u.user_id, u.purpose, u.r2_key, COALESCE(u.preview_type, '')::text AS preview_type, u.full_type::text AS full_type, u.width, u.height,
       u.attached_type, u.attached_id,
       c.tayfa_id AS content_tayfa_id,
       m.role AS member_role, m.status AS member_status, m.muted_until AS member_muted_until,
       COALESCE(tm.enabled, false)::boolean AS events_module_on,
       COALESCE(lm.enabled, false)::boolean AS library_module_on,
       COALESCE(qm.enabled, false)::boolean AS question_pool_module_on,
       COALESCE(mm.enabled, false)::boolean AS materials_module_on,
       (u.attached_type = 'garage_vehicle' AND garage_vehicle_seen_by(u.attached_id, @viewer_id::uuid))::boolean AS garage_visible,
       (u.attached_type = 'user' AND EXISTS (
         SELECT 1 FROM user_blocks b
         WHERE (b.blocker_id = @viewer_id AND b.blocked_id = u.attached_id)
            OR (b.blocker_id = u.attached_id AND b.blocked_id = @viewer_id)
       ))::boolean AS blocked,
       (u.attached_type = 'user' AND EXISTS (
         SELECT 1 FROM user_profiles p WHERE p.user_id = u.attached_id AND p.deleted_at IS NOT NULL
       ))::boolean AS owner_deleted
FROM uploads u
LEFT JOIN LATERAL (
  SELECT t.id AS tayfa_id FROM tayfas t
  WHERE u.attached_type = 'tayfa' AND t.id = u.attached_id AND t.deleted_at IS NULL
  UNION ALL
  SELECT e.tayfa_id FROM events e JOIN tayfas t ON t.id = e.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'event' AND e.id = u.attached_id AND e.deleted_at IS NULL
  UNION ALL
  SELECT e.tayfa_id FROM event_log_entries f
  JOIN events e ON e.id = f.event_id AND e.deleted_at IS NULL
  JOIN tayfas t ON t.id = e.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'log_entry' AND f.id = u.attached_id AND f.deleted_at IS NULL
  UNION ALL
  SELECT e.tayfa_id FROM event_photos ph
  JOIN events e ON e.id = ph.event_id AND e.deleted_at IS NULL
  JOIN tayfas t ON t.id = e.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'event_photo' AND ph.id = u.attached_id AND ph.deleted_at IS NULL
  UNION ALL
  SELECT li.tayfa_id FROM library_items li
  JOIN tayfas t ON t.id = li.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'library_item' AND li.id = u.attached_id AND li.status <> 'withdrawn'
  UNION ALL
  SELECT qq.tayfa_id FROM questions qq
  JOIN tayfas t ON t.id = qq.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'question' AND qq.id = u.attached_id AND qq.deleted_at IS NULL
  UNION ALL
  SELECT aa.tayfa_id FROM answers aa
  JOIN questions qq ON qq.id = aa.question_id AND qq.deleted_at IS NULL
  JOIN tayfas t ON t.id = aa.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'answer' AND aa.id = u.attached_id AND aa.deleted_at IS NULL
  UNION ALL
  SELECT mt.tayfa_id FROM materials mt
  JOIN tayfas t ON t.id = mt.tayfa_id AND t.deleted_at IS NULL
  WHERE u.attached_type = 'material' AND mt.id = u.attached_id AND mt.deleted_at IS NULL
) c ON true
LEFT JOIN memberships m ON m.tayfa_id = c.tayfa_id AND m.user_id = @viewer_id
LEFT JOIN tayfa_modules tm ON tm.tayfa_id = c.tayfa_id AND tm.module = 'events'
LEFT JOIN tayfa_modules lm ON lm.tayfa_id = c.tayfa_id AND lm.module = 'library'
LEFT JOIN tayfa_modules qm ON qm.tayfa_id = c.tayfa_id AND qm.module = 'question_pool'
LEFT JOIN tayfa_modules mm ON mm.tayfa_id = c.tayfa_id AND mm.module = 'materials'
WHERE u.id = ANY(@ids::uuid[]) AND u.status = 'ready' AND u.purpose <> 'data_export';

-- Platform panelinde şikâyet edilen albüm fotoğrafı (ADR-0028 §5): yalnızca
-- panelin şikâyetten okuduğu upload_id, hazır event_photo dosyası.
-- name: GetReportedPhotoUpload :one
SELECT r2_key, full_type::text AS full_type FROM uploads
WHERE id = @id AND purpose = 'event_photo' AND status = 'ready';

-- Platform panelinde şikâyet edilen kanal mesajının fotoğrafı (ADR-0031):
-- şikâyetin kaydettiği upload_id, hâlâ o mesaja bağlı, hazır chat_photo.
-- name: GetReportedMessagePhotoUpload :one
SELECT r2_key FROM uploads
WHERE id = @id AND purpose = 'chat_photo' AND status = 'ready'
  AND attached_type = 'channel_message' AND attached_id = @message_id;

-- İçeriğin dosyası. Kolonun trigger'ı (uploads_check_ref) dosyanın bu
-- içeriğe bağlı ve hazır olduğunu yeniden doğrular; eski dosya
-- uploads_release_ref ile silme kuyruğuna girer.
-- name: SetTayfaCover :exec
UPDATE tayfas SET cover_upload_id = sqlc.narg(cover_upload_id)
WHERE id = @id AND deleted_at IS NULL;

-- name: SetEventCover :exec
UPDATE events SET cover_upload_id = sqlc.narg(cover_upload_id)
WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- KVKK veri dışa aktarma dosyası (issue #144, ADR-0021). Yalnızca çağıranın
-- kendi data_export kaydı; görüntü yolları bu kayıtları görmez.

-- Yeni yuva açılmadan önce kullanıcının başka etkin dışa aktarması
-- silinir: identity aynı anda tek talep yürütür, eskisi artıktır.
-- name: ReleaseOtherExports :execrows
UPDATE uploads SET status = 'deleting'
WHERE user_id = @user_id AND purpose = 'data_export' AND status IN ('pending', 'ready')
  AND client_request_id <> @client_request_id;

-- name: CreateExportUpload :one
INSERT INTO uploads (id, user_id, purpose, r2_key, client_request_id)
VALUES (@id, @user_id, 'data_export', @r2_key, @client_request_id)
ON CONFLICT (user_id, client_request_id) DO NOTHING
RETURNING id;

-- name: GetExportByClientRequest :one
SELECT * FROM uploads
WHERE user_id = @user_id AND client_request_id = @client_request_id AND purpose = 'data_export';

-- name: GetOwnExport :one
SELECT * FROM uploads
WHERE id = @id AND user_id = @user_id AND purpose = 'data_export' AND status <> 'deleting';

-- Boyut bir kez yazılır (trigger uploads_export_bytes_once); aynı boyut
-- tekrar yazılabilir.
-- name: SetExportBytes :execrows
UPDATE uploads SET export_bytes = @export_bytes
WHERE id = @id AND user_id = @user_id AND purpose = 'data_export' AND status = 'pending'
  AND (export_bytes IS NULL OR export_bytes = @export_bytes);

-- name: MarkExportReady :execrows
UPDATE uploads SET status = 'ready', ready_at = @now, expires_at = @expires_at
WHERE id = @id AND user_id = @user_id AND purpose = 'data_export' AND status = 'pending'
  AND export_bytes IS NOT NULL;

-- Yanlış boyutta yüklenmiş dışa aktarma silinir.
-- name: DropExport :execrows
UPDATE uploads SET status = 'deleting'
WHERE id = @id AND user_id = @user_id AND purpose = 'data_export' AND status <> 'deleting';
