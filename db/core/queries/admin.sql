-- Platform yönetim paneli (issue #104 2. aşama, ADR-0008). Yalnızca
-- internal/core/app/admin kullanır; her çağrıdan önce admin token'ı ve
-- identity'nin yöneticilik onayı denetlenir. Panelin göremedikleri burada
-- seçilmez: konum (buluşma noktası, av koordinatı), anket oyları, tayfa
-- içeriği (yalnızca şikâyet edilen öğe tek tek okunur).

-- name: AdminSearchTayfas :many
SELECT t.id, t.name, t.status, t.visibility, t.created_at, t.suspended_at, t.deleted_at,
       t.deletion_scheduled_for,
       (SELECT count(*) FROM memberships m WHERE m.tayfa_id = t.id AND m.status = 'active')::int AS member_count
FROM tayfas t
WHERE t.name_normalized LIKE '%' || @pattern::text || '%' ESCAPE '\'
ORDER BY t.created_at DESC, t.id DESC
LIMIT 50;

-- name: AdminGetTayfa :one
SELECT t.id, t.name, t.status, t.visibility, t.created_at, t.suspended_at, t.deleted_at,
       t.deletion_scheduled_for, t.description, t.city, t.district, t.suspended_reason,
       (SELECT count(*) FROM memberships m WHERE m.tayfa_id = t.id AND m.status = 'active')::int AS member_count,
       (SELECT count(*) FROM reports r WHERE r.tayfa_id = t.id AND r.platform_status = 'open')::int AS open_platform_reports
FROM tayfas t
WHERE t.id = @id;

-- Banlı üyede son banın gerekçesi denetim kaydından (issue #150; yalnızca
-- sınıflandırma, serbest metin değil). 365 günlük saklamadan sonra boştur.
-- name: AdminListTayfaMembers :many
SELECT m.user_id, p.display_name, m.role, m.status, m.joined_at,
       COALESCE(b.reason, '') AS ban_reason
FROM memberships m
LEFT JOIN user_profiles p ON p.user_id = m.user_id AND p.deleted_at IS NULL
LEFT JOIN LATERAL (
  SELECT (a.metadata ->> 'reason')::text AS reason
  FROM audit_logs a
  WHERE m.status = 'banned' AND a.tayfa_id = m.tayfa_id AND a.action = 'member.banned'
    AND a.target_type = 'membership' AND a.target_id = m.user_id
  ORDER BY a.created_at DESC, a.id DESC
  LIMIT 1
) b ON true
WHERE m.tayfa_id = @tayfa_id
  AND (NOT @staff_only::boolean OR (m.status = 'active' AND m.role IN ('admin', 'moderator')))
ORDER BY role_rank(m.role) DESC, m.joined_at NULLS LAST, m.user_id
LIMIT 500;

-- name: AdminListUserTayfas :many
SELECT t.id, t.name, t.status, t.visibility, t.created_at, t.suspended_at, t.deleted_at,
       t.deletion_scheduled_for, m.role,
       (SELECT count(*) FROM memberships x WHERE x.tayfa_id = t.id AND x.status = 'active')::int AS member_count
FROM memberships m
JOIN tayfas t ON t.id = m.tayfa_id
WHERE m.user_id = @user_id AND m.status = 'active'
ORDER BY t.name_normalized, t.id
LIMIT 500;

-- name: AdminLockTayfa :one
SELECT id, suspended_at, deleted_at FROM tayfas WHERE id = @id FOR NO KEY UPDATE;

-- name: AdminSuspendTayfa :execrows
UPDATE tayfas SET suspended_at = @at, suspended_reason = @reason, suspended_by = @by
WHERE id = @id AND suspended_at IS NULL AND deleted_at IS NULL;

-- name: AdminUnsuspendTayfa :execrows
UPDATE tayfas SET suspended_at = NULL, suspended_reason = NULL, suspended_by = NULL
WHERE id = @id AND suspended_at IS NOT NULL AND deleted_at IS NULL;

-- 72 saat beklemeden soft delete; 30 gün sonra mevcut iş kalıcı siler.
-- name: AdminDeleteTayfa :execrows
UPDATE tayfas SET deleted_at = @at, deleted_by = @by
WHERE id = @id AND deleted_at IS NULL;

-- Yalnızca aktif üyenin rolü (00014 'platform_admin' sistem işi).
-- name: AdminSetMemberRole :execrows
UPDATE memberships SET role = @role
WHERE tayfa_id = @tayfa_id AND user_id = @user_id AND status = 'active' AND role <> @role;

-- name: AdminMemberRole :one
SELECT role, status FROM memberships WHERE tayfa_id = @tayfa_id AND user_id = @user_id;

-- name: AdminTayfaStaffIDs :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND role IN ('admin', 'moderator')
ORDER BY role_rank(role) DESC, user_id
LIMIT 100;

-- Platform kuyruğu: açıklar önce, içlerinde minor_involved ve child_safety
-- önce, sonra yükselme sırasıyla.
-- name: AdminListPlatformReports :many
SELECT id, tayfa_id, target_type, target_id, subject_user_id, reporter_id, reason, description,
       priority, created_at, escalated_to_platform_at, platform_status, platform_decision, platform_handled_at
FROM reports
WHERE escalated_to_platform_at IS NOT NULL AND (@include_resolved::boolean OR platform_status = 'open')
ORDER BY (platform_status = 'open') DESC, (priority = 'minor_involved' OR reason = 'child_safety') DESC,
         escalated_to_platform_at, id
LIMIT 200;

-- name: AdminGetPlatformReport :one
SELECT id, tayfa_id, target_type, target_id, subject_user_id, reporter_id, reason, description,
       priority, created_at, escalated_to_platform_at, platform_status, platform_decision, platform_handled_at,
       evidence_text, status
FROM reports
WHERE id = @id AND escalated_to_platform_at IS NOT NULL;

-- name: AdminLockPlatformReport :one
SELECT id FROM reports WHERE id = @id AND escalated_to_platform_at IS NOT NULL FOR UPDATE;

-- Platform kararı. Tayfada henüz açıksa şikâyet de çözülür (çözen tayfa
-- yetkilisi değil: handled_by boş kalır).
-- name: AdminDecideReport :execrows
UPDATE reports
SET platform_status = 'resolved', platform_decision = @decision, platform_handled_by = @by, platform_handled_at = @at,
    status = 'resolved',
    resolution = CASE WHEN status = 'open' THEN @resolution::text ELSE resolution END,
    handled_at = CASE WHEN status = 'open' THEN @at ELSE handled_at END
WHERE id = @id AND platform_status = 'open';

-- Şikâyet edilen öğe, yalnızca o öğe. Konum ve oy alanları seçilmez.

-- name: AdminGetAnnouncement :one
SELECT author_id, title, body, created_at, deleted_at FROM announcements WHERE id = @id AND tayfa_id = @tayfa_id;

-- name: AdminGetEvent :one
SELECT created_by, title, description, created_at, deleted_at FROM events WHERE id = @id AND tayfa_id = @tayfa_id;

-- name: AdminGetQuestion :one
SELECT author_id, title, body, created_at, deleted_at FROM questions WHERE id = @id AND tayfa_id = @tayfa_id;

-- name: AdminGetAnswer :one
SELECT author_id, body, created_at, deleted_at FROM answers WHERE id = @id AND tayfa_id = @tayfa_id;

-- name: AdminGetPoll :one
SELECT created_by, question, created_at, deleted_at FROM polls WHERE id = @id AND tayfa_id = @tayfa_id;

-- name: AdminGetPollOptions :many
SELECT text FROM poll_options WHERE poll_id = @poll_id ORDER BY position;

-- name: AdminGetCatch :one
SELECT c.user_id, c.species, c.created_at, c.deleted_at
FROM fishing_catches c JOIN events e ON e.id = c.event_id
WHERE c.id = @id AND e.tayfa_id = @tayfa_id;

-- name: AdminGetProfileName :one
SELECT display_name FROM user_profiles WHERE user_id = @user_id AND deleted_at IS NULL;

-- İçeriği kaldırma: soft delete, silen platform yöneticisi.

-- name: AdminDeleteAnnouncement :execrows
UPDATE announcements SET deleted_at = @at, deleted_by = @by WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- name: AdminDeleteEvent :execrows
UPDATE events SET deleted_at = @at, deleted_by = @by WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- name: AdminDeleteQuestion :execrows
UPDATE questions SET deleted_at = @at, deleted_by = @by WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- name: AdminDeleteAnswer :execrows
UPDATE answers SET deleted_at = @at, deleted_by = @by WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- name: AdminDeletePoll :execrows
UPDATE polls SET deleted_at = @at, deleted_by = @by WHERE id = @id AND tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- name: AdminDeleteCatch :execrows
UPDATE fishing_catches c SET deleted_at = @at, deleted_by = @by
FROM events e
WHERE c.id = @id AND e.id = c.event_id AND e.tayfa_id = @tayfa_id AND c.deleted_at IS NULL;

-- Platform banı (identity'nin user.banned / user.unbanned olayları). Eski
-- olay yenisini ezmez; aynı olay ikinci kez uygulanınca değişmez.
-- name: ApplyUserBan :execrows
INSERT INTO user_platform_bans (user_id, banned_at, event_id)
VALUES (@user_id, sqlc.narg('banned_at'), @event_id)
ON CONFLICT (user_id) DO UPDATE
SET banned_at = EXCLUDED.banned_at, event_id = EXCLUDED.event_id
WHERE user_platform_bans.event_id < EXCLUDED.event_id;

-- name: DeleteUserBan :exec
DELETE FROM user_platform_bans WHERE user_id = @user_id;

-- Pano sayaçları (issue #110). Yalnızca sayılar; tayfa kimliği ya da adı
-- dönmez.
-- name: AdminTayfaStats :one
SELECT
  count(*)::bigint AS total,
  count(*) FILTER (WHERE suspended_at IS NULL AND status = 'active')::bigint AS active,
  count(*) FILTER (WHERE suspended_at IS NOT NULL)::bigint AS suspended,
  count(*) FILTER (WHERE status = 'warned')::bigint AS warned,
  count(*) FILTER (WHERE status = 'archived')::bigint AS archived
FROM tayfas
WHERE deleted_at IS NULL;

-- name: AdminListJobRuns :many
SELECT job, finished_at, succeeded, changed, last_succeeded_at
FROM job_runs
ORDER BY job;
