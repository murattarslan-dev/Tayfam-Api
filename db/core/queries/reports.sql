-- Şikâyetler ve tayfa moderasyon kuyruğu (issue #76). Her sorgu tayfa_id ya
-- da şikâyet eden ile sınırlıdır (IDOR); üyelik ve izin kontrolü app
-- katmanındadır (access, authz). Şikâyet edenin kimliği kuyruk sorgularından
-- hiç okunmaz.

-- Hedefler: şikâyet edilebilmesi için çağıranın görebildiği bir içerik
-- olmalıdır. Her sorgu yazarı ve görünür olup olmadığını döndürür.

-- name: GetReportTargetAnnouncement :one
SELECT author_id, (deleted_at IS NULL)::boolean AS visible
FROM announcements
WHERE tayfa_id = @tayfa_id AND id = @id;

-- İptal edilmiş etkinlik görünür kalır; kaldırılmış sayılır (çözüm için).
-- name: GetReportTargetEvent :one
SELECT created_by AS author_id, (deleted_at IS NULL)::boolean AS visible, (status = 'cancelled')::boolean AS cancelled
FROM events
WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: GetReportTargetQuestion :one
SELECT author_id, (deleted_at IS NULL)::boolean AS visible
FROM questions
WHERE tayfa_id = @tayfa_id AND id = @id;

-- Silinmiş sorunun cevabı da görünmez.
-- name: GetReportTargetAnswer :one
SELECT a.author_id, (a.deleted_at IS NULL AND q.deleted_at IS NULL)::boolean AS visible
FROM answers a
JOIN questions q ON q.id = a.question_id AND q.tayfa_id = a.tayfa_id
WHERE a.tayfa_id = @tayfa_id AND a.id = @id;

-- name: GetReportTargetPoll :one
SELECT created_by AS author_id, (deleted_at IS NULL)::boolean AS visible
FROM polls
WHERE tayfa_id = @tayfa_id AND id = @id;

-- Av kaydı: etkinliği de görünür olmalı; 'self' görünürlüğündeki kaydı
-- yalnızca sahibi görür (app katmanı kontrol eder).
-- name: GetReportTargetCatch :one
SELECT c.user_id AS author_id, (c.deleted_at IS NULL AND e.deleted_at IS NULL)::boolean AS visible, c.visibility
FROM fishing_catches c
JOIN events e ON e.id = c.event_id
WHERE e.tayfa_id = @tayfa_id AND c.id = @id;

-- Şikâyet edenin son 24 saatteki şikâyetleri (günlük sınır).
-- name: CountRecentReportsByReporter :one
SELECT count(*)::integer
FROM reports
WHERE reporter_id = @reporter_id AND created_at > @since;

-- Aynı hedefe ikinci şikâyet mevcut kaydı döndürür.
-- name: CreateReport :one
INSERT INTO reports (
  id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason, description,
  priority, minor_involved_at, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at)
VALUES (
  @id, @tayfa_id, @reporter_id, @target_type, @target_id, @subject_user_id, @reason, sqlc.narg(description),
  @priority, sqlc.narg(minor_involved_at), @platform_only, sqlc.narg(escalated_to_platform_at),
  sqlc.narg(escalation_reason), sqlc.narg(platform_status), @created_at)
ON CONFLICT (reporter_id, target_type, target_id) DO NOTHING
RETURNING id;

-- Şikâyet edenin kendi şikâyeti.
-- name: GetReportOfReporter :one
SELECT id, tayfa_id, target_type, target_id, reason, description, status, resolution, handled_at, created_at
FROM reports
WHERE reporter_id = @reporter_id AND id = @id;

-- name: GetReportOfReporterByTarget :one
SELECT id, tayfa_id, target_type, target_id, reason, description, status, resolution, handled_at, created_at
FROM reports
WHERE reporter_id = @reporter_id AND target_type = @target_type AND target_id = @target_id;

-- Şikâyet edenin şikâyetleri, yeniden eskiye.
-- name: ListReportsOfReporter :many
SELECT id, tayfa_id, target_type, target_id, reason, description, status, resolution, handled_at, created_at
FROM reports
WHERE reporter_id = @reporter_id
  AND (NOT @has_cursor::boolean OR (created_at, id) < (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- Kuyruk: tayfanın açık şikâyetleri; platforma özel olanlar ve izleyicinin
-- kendisi hakkındakiler hariç. Eskiden yeniye. reporter_id, priority ve
-- yükselme bilgisi seçilmez (ADR-0012: tayfa yöneticileri 18 altı bilgisini
-- görmez; yükselme zamanı önceliği ele verirdi).
-- name: ListOpenTayfaReports :many
SELECT id, tayfa_id, target_type, target_id, subject_user_id, reason, description, status, resolution,
       handled_by, handled_at, created_at
FROM reports
WHERE tayfa_id = @tayfa_id AND status = 'open' AND NOT platform_only
  AND subject_user_id IS DISTINCT FROM @viewer_id::uuid
  AND (NOT @has_cursor::boolean OR (created_at, id) > (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY created_at, id
LIMIT @page_size;

-- Çözülmüşler, son çözülen önce.
-- name: ListResolvedTayfaReports :many
SELECT id, tayfa_id, target_type, target_id, subject_user_id, reason, description, status, resolution,
       handled_by, handled_at, created_at
FROM reports
WHERE tayfa_id = @tayfa_id AND status = 'resolved' AND NOT platform_only
  AND subject_user_id IS DISTINCT FROM @viewer_id::uuid
  AND (NOT @has_cursor::boolean OR (handled_at, id) < (@after_handled_at::timestamptz, @after_id::uuid))
ORDER BY handled_at DESC, id DESC
LIMIT @page_size;

-- Çözüm için kilitlenir; kuyruğun kuralları aynı (platforma özel ve
-- izleyicinin kendisi hakkındaki şikâyet bulunamaz).
-- name: LockTayfaReport :one
SELECT id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason, description, status,
       resolution, handled_by, handled_at, created_at
FROM reports
WHERE tayfa_id = @tayfa_id AND id = @id AND NOT platform_only
  AND subject_user_id IS DISTINCT FROM @viewer_id::uuid
FOR UPDATE;

-- name: ResolveReport :execrows
UPDATE reports
SET status = 'resolved', resolution = @resolution, handled_by = @handled_by, handled_at = @handled_at
WHERE tayfa_id = @tayfa_id AND id = @id AND status = 'open';

-- ---------------------------------------------------------------------------
-- Arka plan işleri
-- ---------------------------------------------------------------------------

-- 7 gün çözülmeyen şikâyetler platforma yükselir.
-- name: EscalateUnresolvedReports :many
UPDATE reports
SET escalated_to_platform_at = @now, escalation_reason = 'unresolved', platform_status = 'open'
WHERE id IN (
    SELECT r.id FROM reports r
    WHERE r.status = 'open' AND r.escalated_to_platform_at IS NULL AND r.created_at <= @created_before
    ORDER BY r.created_at, r.id
    LIMIT @n
    FOR UPDATE SKIP LOCKED
  )
  AND status = 'open' AND escalated_to_platform_at IS NULL AND created_at <= @created_before
RETURNING id, tayfa_id, priority;

-- Çözülmüş şikâyetler 1 yıl sonra silinir; platformda açık olan kalır.
-- name: DeleteExpiredReports :execrows
DELETE FROM reports
WHERE id IN (
    SELECT r.id FROM reports r
    WHERE r.status = 'resolved' AND r.handled_at <= @handled_before
      AND (r.platform_status IS NULL OR r.platform_status <> 'open')
    ORDER BY r.handled_at, r.id
    LIMIT @n
    FOR UPDATE SKIP LOCKED
  )
  AND status = 'resolved' AND handled_at <= @handled_before
  AND (platform_status IS NULL OR platform_status <> 'open');

-- audit_logs saklama işi; trigger yalnızca bu ayarla ve 365 günden eski
-- kayıtlar için silmeye izin verir.
-- name: SetAuditRetentionJob :exec
SELECT set_config('app.retention_job', 'audit_logs', true);

-- name: DeleteExpiredAuditLogs :execrows
DELETE FROM audit_logs
WHERE id IN (
    SELECT a.id FROM audit_logs a
    WHERE a.created_at < @created_before
    ORDER BY a.created_at, a.id
    LIMIT @n
    FOR UPDATE SKIP LOCKED
  )
  AND created_at < @created_before;

-- ---------------------------------------------------------------------------
-- Hesap silme (SetSystemJob('account_deletion') sonrası)
-- ---------------------------------------------------------------------------

-- name: ClearReportUserFields :exec
UPDATE reports
SET reporter_id         = NULLIF(reporter_id, @user_id),
    subject_user_id     = NULLIF(subject_user_id, @user_id),
    handled_by          = NULLIF(handled_by, @user_id),
    platform_handled_by = NULLIF(platform_handled_by, @user_id)
WHERE reporter_id = @user_id OR subject_user_id = @user_id
   OR handled_by = @user_id OR platform_handled_by = @user_id;
