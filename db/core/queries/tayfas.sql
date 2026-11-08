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
