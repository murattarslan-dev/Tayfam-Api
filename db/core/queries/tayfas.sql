-- Tayfa, kurucu üyelik, modüller ve ilgi alanları tek transaction'da
-- yazılır; birleştirmeyi app katmanı yapar.
-- name: CreateTayfa :exec
INSERT INTO tayfas (id, name, name_normalized, description, join_code, join_policy, created_by, client_request_id)
VALUES (@id, @name, @name_normalized, @description, @join_code, @join_policy, @created_by, @client_request_id);

-- name: GetTayfa :one
SELECT id, name, name_normalized, description, join_code, status, visibility,
       join_policy, city, district, created_by, last_activity_at, suspended_at,
       deletion_requested_at, deletion_scheduled_for, created_at, updated_at
FROM tayfas
WHERE id = @id AND deleted_at IS NULL;

-- Tekrar gönderilen oluşturma isteği aynı tayfayı döndürür.
-- name: GetTayfaByClientRequest :one
SELECT id FROM tayfas
WHERE created_by = @created_by AND client_request_id = @client_request_id;

-- name: GetTayfaByJoinCode :one
SELECT id, join_policy, status, suspended_at
FROM tayfas
WHERE join_code = @join_code AND deleted_at IS NULL;

-- Kullanıcının aktif üye olduğu tayfalar; tayfa ID'sine göre sayfalanır.
-- name: ListUserActiveTayfas :many
SELECT t.id, t.name, t.description, t.status, t.join_policy, t.suspended_at,
       t.deletion_scheduled_for, m.role, m.muted_until
FROM memberships m
JOIN tayfas t ON t.id = m.tayfa_id
WHERE m.user_id = @user_id
  AND m.status = 'active'
  AND t.deleted_at IS NULL
  AND t.id > @after_id
ORDER BY t.id
LIMIT @page_size;

-- Kullanıcı başına günlük tayfa açma sınırı. Silinen tayfalar da sayılır;
-- açıp silerek sınır aşılamaz.
-- name: CountTayfasCreatedSince :one
SELECT count(*) FROM tayfas
WHERE created_by = @created_by AND created_at > @since;

-- name: AddTayfaInterest :exec
INSERT INTO tayfa_interests (tayfa_id, event_type)
VALUES (@tayfa_id, @event_type)
ON CONFLICT DO NOTHING;

-- name: DeleteTayfaInterests :exec
DELETE FROM tayfa_interests WHERE tayfa_id = @tayfa_id;

-- name: ListTayfaInterests :many
SELECT event_type FROM tayfa_interests WHERE tayfa_id = @tayfa_id ORDER BY event_type;

-- name: SetTayfaModule :exec
INSERT INTO tayfa_modules (tayfa_id, module, enabled)
VALUES (@tayfa_id, @module, @enabled)
ON CONFLICT (tayfa_id, module) DO UPDATE SET enabled = EXCLUDED.enabled;

-- name: ListTayfaModules :many
SELECT module, enabled FROM tayfa_modules WHERE tayfa_id = @tayfa_id ORDER BY module;

-- Yazan işlemler tayfa satırını kilitleyerek okur; memberships trigger'ı da
-- aynı satırı kilitlediği için üyelik değişiklikleriyle sırayla çalışır.
-- name: GetTayfaForUpdate :one
SELECT id, name, name_normalized, description, join_code, status, visibility,
       join_policy, city, district, created_by, last_activity_at, suspended_at,
       deletion_requested_at, deletion_scheduled_for, created_at, updated_at
FROM tayfas
WHERE id = @id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- Aynı kullanıcının eşzamanlı oluşturma istekleri sırayla işlenir; günlük
-- sınır iki istekte birden aşılamaz. Kilit transaction bitince bırakılır.
-- name: LockTayfaCreation :exec
SELECT pg_advisory_xact_lock(hashtextextended(@lock_key::text, 0));

-- name: GetTayfaByClientRequestID :one
SELECT id, deleted_at FROM tayfas
WHERE created_by = @created_by AND client_request_id = @client_request_id;

-- name: UpdateTayfaSettings :execrows
UPDATE tayfas
SET name = @name, name_normalized = @name_normalized,
    description = @description, join_policy = @join_policy
WHERE id = @id AND deleted_at IS NULL;

-- name: SetTayfaJoinCode :execrows
UPDATE tayfas SET join_code = @join_code
WHERE id = @id AND deleted_at IS NULL;

-- name: RequestTayfaDeletion :execrows
UPDATE tayfas
SET deletion_requested_at = @requested_at, deletion_requested_by = @requested_by,
    deletion_scheduled_for = @scheduled_for
WHERE id = @id AND deleted_at IS NULL AND deletion_requested_at IS NULL;

-- name: CancelTayfaDeletion :execrows
UPDATE tayfas
SET deletion_requested_at = NULL, deletion_requested_by = NULL,
    deletion_scheduled_for = NULL
WHERE id = @id AND deleted_at IS NULL AND deletion_requested_at IS NOT NULL;

-- "Tayfalarım": kullanıcının aktif üyelikleri ve bekleyen başvuruları, en
-- yeni üyelik kaydı önce; (created_at, tayfa_id) ile sayfalanır.
-- name: ListMyTayfas :many
SELECT t.id, t.name, t.name_normalized, t.description, t.join_code, t.status,
       t.join_policy, t.suspended_at, t.deletion_scheduled_for, t.created_at,
       t.updated_at, m.role, m.status AS membership_status, m.muted_until,
       m.created_at AS membership_created_at
FROM memberships m
JOIN tayfas t ON t.id = m.tayfa_id
WHERE m.user_id = @user_id
  AND m.status IN ('active', 'pending')
  AND t.deleted_at IS NULL
  AND (m.created_at, m.tayfa_id) < (@before_created_at::timestamptz, @before_tayfa_id::uuid)
ORDER BY m.created_at DESC, m.tayfa_id DESC
LIMIT @page_size;

-- name: ListInterestsForTayfas :many
SELECT tayfa_id, event_type FROM tayfa_interests
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]);

-- name: ListModulesForTayfas :many
SELECT tayfa_id, module, enabled FROM tayfa_modules
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]);

-- name: CountActiveMembersForTayfas :many
SELECT tayfa_id, count(*) AS member_count FROM memberships
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]) AND status = 'active'
GROUP BY tayfa_id;

-- Kapalı ya da kaydı olmayan modül kapalı sayılır (varsayılan "hayır").
-- name: IsTayfaModuleEnabled :one
SELECT EXISTS (
  SELECT 1 FROM tayfa_modules
  WHERE tayfa_id = @tayfa_id AND module = @module AND enabled
)::boolean AS enabled;
