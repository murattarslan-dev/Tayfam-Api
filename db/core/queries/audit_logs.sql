-- Denetim kaydı yalnızca eklenir. metadata'ya kişisel veri ya da token
-- yazılmaz.
-- name: CreateAuditLog :exec
INSERT INTO audit_logs (id, tayfa_id, actor_id, action, target_type, target_id, metadata)
VALUES (@id, @tayfa_id, @actor_id, @action, @target_type, @target_id, @metadata);

-- name: ListAuditLogs :many
SELECT id, actor_id, action, target_type, target_id, metadata, created_at
FROM audit_logs
WHERE tayfa_id = @tayfa_id
  AND (created_at, id) < (@before_created_at::timestamptz, @before_id::uuid)
ORDER BY created_at DESC, id DESC
LIMIT @page_size;
