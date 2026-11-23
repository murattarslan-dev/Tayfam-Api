-- Materyal kütüphanesi (issue #261; ADR-0040 §3). Her sorgu tayfa_id ile
-- çalışır. Silinmiş materyal ve engelli alan adındaki bağlantı
-- (material_domain_blocked) okunmaz.

-- Tayfanın görünür materyalleri, en yeni önce; süzgeçler isteğe bağlı.
-- name: ListMaterialsPage :many
SELECT m.id, m.author_id, m.kind, m.title, m.url AS link, m.domain,
       m.photo1_upload_id, m.photo2_upload_id, m.photo3_upload_id,
       m.subject_key, m.topic_key, m.created_at, m.updated_at
FROM materials m
WHERE m.tayfa_id = @tayfa_id
  AND m.deleted_at IS NULL
  AND NOT material_domain_blocked(m.domain)
  AND (sqlc.narg(material_id)::uuid IS NULL OR m.id = sqlc.narg(material_id)::uuid)
  AND (sqlc.narg(kind)::text IS NULL OR m.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(subject_key)::text IS NULL OR m.subject_key = sqlc.narg(subject_key)::text)
  AND (sqlc.narg(topic_key)::text IS NULL OR m.topic_key = sqlc.narg(topic_key)::text)
  AND (sqlc.narg(author_id)::uuid IS NULL OR m.author_id = sqlc.narg(author_id)::uuid)
  AND (NOT @has_cursor::boolean
       OR (m.created_at, m.id) < (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY m.created_at DESC, m.id DESC
LIMIT @page_size;

-- name: GetMaterialByClientRequest :one
SELECT id FROM materials WHERE tayfa_id = @tayfa_id AND client_request_id = @client_request_id;

-- Yazarın tayfadaki silinmemiş materyalleri.
-- name: CountAuthorMaterials :one
SELECT count(*)::integer FROM materials
WHERE tayfa_id = @tayfa_id AND author_id = @author_id AND deleted_at IS NULL;

-- name: CreateMaterial :exec
INSERT INTO materials (id, tayfa_id, author_id, kind, title, url, domain,
                       photo1_upload_id, photo2_upload_id, photo3_upload_id,
                       subject_key, topic_key, client_request_id, created_at, updated_at)
VALUES (@id, @tayfa_id, @author_id, @kind, @title, sqlc.narg(link), sqlc.narg(domain),
        sqlc.narg(photo1_upload_id), sqlc.narg(photo2_upload_id), sqlc.narg(photo3_upload_id),
        @subject_key, sqlc.narg(topic_key), @client_request_id, @now, @now);

-- Değiştirilecek materyal, kilitli. `blocked`: bağlantının alan adı engelli
-- (materyal gizlidir).
-- name: GetMaterialForUpdate :one
SELECT m.id, m.author_id, m.kind, m.url AS link, m.domain,
       m.photo1_upload_id, m.photo2_upload_id, m.photo3_upload_id,
       m.subject_key, m.topic_key, m.deleted_at,
       material_domain_blocked(m.domain)::boolean AS blocked
FROM materials m
WHERE m.tayfa_id = @tayfa_id AND m.id = @id
FOR UPDATE;

-- name: UpdateMaterial :execrows
UPDATE materials
SET title = @title, url = sqlc.narg(link), domain = sqlc.narg(domain),
    photo1_upload_id = sqlc.narg(photo1_upload_id), photo2_upload_id = sqlc.narg(photo2_upload_id),
    photo3_upload_id = sqlc.narg(photo3_upload_id),
    subject_key = @subject_key, topic_key = sqlc.narg(topic_key), updated_at = @now
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Soft delete. Yazarın kendi silmesinde (`release_photos`) fotoğraflar
-- hemen bırakılır; dosyalar silme kuyruğuna girer (uploads_release_ref).
-- name: DeleteMaterial :execrows
UPDATE materials
SET deleted_at = @now, deleted_by = @deleted_by,
    photo1_upload_id = CASE WHEN @release_photos::boolean THEN NULL ELSE photo1_upload_id END,
    photo2_upload_id = CASE WHEN @release_photos::boolean THEN NULL ELSE photo2_upload_id END,
    photo3_upload_id = CASE WHEN @release_photos::boolean THEN NULL ELSE photo3_upload_id END
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Bağlantının alan adı ya da üst alan adlarından biri engelli mi.
-- name: MaterialDomainBlocked :one
SELECT material_domain_blocked(@domain::text)::boolean;
