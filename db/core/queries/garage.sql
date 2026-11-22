-- Üye garajı (issue #272; ADR-0040 §4). Garaj kişiye aittir: sahibinin
-- sorguları owner_id ile, tayfadaki görünüm tayfa_id + sahibinin etkin
-- üyeliği + garage_visible_in ile çalışır. Silinmiş araç okunmaz.

-- Sahibinin aynı anda gelen yazımları sınırı birlikte aşamaz.
-- name: LockGarage :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.core.garage:' || sqlc.arg(owner_id)::uuid::text, 0));

-- Sahibinin silinmemiş araçları, eklenme sırasıyla; vehicle_id verilirse
-- yalnızca o araç.
-- name: ListOwnGarageVehicles :many
SELECT g.id, g.owner_id, g.make, g.model, g.model_year, g.color, g.note,
       g.photo1_upload_id, g.photo2_upload_id, g.photo3_upload_id, g.created_at, g.updated_at
FROM garage_vehicles g
WHERE g.owner_id = @owner_id AND g.deleted_at IS NULL
  AND (sqlc.narg(vehicle_id)::uuid IS NULL OR g.id = sqlc.narg(vehicle_id)::uuid)
ORDER BY g.created_at, g.id;

-- name: CountOwnGarageVehicles :one
SELECT count(*)::integer FROM garage_vehicles WHERE owner_id = @owner_id AND deleted_at IS NULL;

-- name: GetGarageVehicleByClientRequest :one
SELECT id FROM garage_vehicles WHERE owner_id = @owner_id AND client_request_id = @client_request_id;

-- name: CreateGarageVehicle :exec
INSERT INTO garage_vehicles (id, owner_id, make, model, model_year, color, note,
                             photo1_upload_id, photo2_upload_id, photo3_upload_id,
                             client_request_id, created_at, updated_at)
VALUES (@id, @owner_id, @make, sqlc.narg(model), sqlc.narg(model_year), sqlc.narg(color), sqlc.narg(note),
        sqlc.narg(photo1_upload_id), sqlc.narg(photo2_upload_id), sqlc.narg(photo3_upload_id),
        @client_request_id, @now, @now);

-- Sahibinin değiştireceği araç, kilitli.
-- name: GetOwnGarageVehicleForUpdate :one
SELECT g.id, g.photo1_upload_id, g.photo2_upload_id, g.photo3_upload_id
FROM garage_vehicles g
WHERE g.owner_id = @owner_id AND g.id = @id AND g.deleted_at IS NULL
FOR UPDATE;

-- name: UpdateGarageVehicle :execrows
UPDATE garage_vehicles
SET make = @make, model = sqlc.narg(model), model_year = sqlc.narg(model_year), color = sqlc.narg(color),
    note = sqlc.narg(note), photo1_upload_id = sqlc.narg(photo1_upload_id),
    photo2_upload_id = sqlc.narg(photo2_upload_id), photo3_upload_id = sqlc.narg(photo3_upload_id),
    updated_at = @now
WHERE owner_id = @owner_id AND id = @id AND deleted_at IS NULL;

-- Sahibinin kendi silmesi: soft delete, fotoğraflar hemen bırakılır
-- (dosyalar silme kuyruğuna girer; garage_vehicles_release_photos).
-- name: DeleteOwnGarageVehicle :execrows
UPDATE garage_vehicles
SET deleted_at = @now, deleted_by = @owner_id,
    photo1_upload_id = NULL, photo2_upload_id = NULL, photo3_upload_id = NULL
WHERE owner_id = @owner_id AND id = @id AND deleted_at IS NULL;

-- Tayfada görünen araçlar, en yeni önce: sahibi tayfanın etkin üyesi ve
-- garajı bu tayfada görünür. owner_id verilirse yalnızca o kişinin.
-- name: ListTayfaGarageVehicles :many
SELECT g.id, g.owner_id, g.make, g.model, g.model_year, g.color, g.note,
       g.photo1_upload_id, g.photo2_upload_id, g.photo3_upload_id, g.created_at, g.updated_at
FROM garage_vehicles g
JOIN memberships o ON o.tayfa_id = @tayfa_id::uuid AND o.user_id = g.owner_id AND o.status = 'active'
WHERE g.deleted_at IS NULL
  AND garage_visible_in(g.owner_id, @tayfa_id::uuid)
  AND (sqlc.narg(owner_id)::uuid IS NULL OR g.owner_id = sqlc.narg(owner_id)::uuid)
  AND (sqlc.narg(vehicle_id)::uuid IS NULL OR g.id = sqlc.narg(vehicle_id)::uuid)
  AND (NOT @has_cursor::boolean
       OR (g.created_at, g.id) < (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY g.created_at DESC, g.id DESC
LIMIT @page_size;

-- Moderatörün kaldırması: yalnızca gizler (fotoğraflar kalıcı silmeye kadar
-- kalır). Araç bu tayfada görünür olmalıdır.
-- name: RemoveTayfaGarageVehicle :execrows
UPDATE garage_vehicles g
SET deleted_at = @now::timestamptz, deleted_by = @deleted_by::uuid
WHERE g.id = @id::uuid AND g.deleted_at IS NULL
  AND garage_visible_in(g.owner_id, @tayfa_id::uuid)
  AND EXISTS (SELECT 1 FROM memberships o WHERE o.tayfa_id = @tayfa_id::uuid AND o.user_id = g.owner_id AND o.status = 'active');

-- Kişinin garajı tayfada görünür mü (satır ya da varsayılan).
-- name: GarageVisibleIn :one
SELECT garage_visible_in(@owner_id::uuid, @tayfa_id::uuid)::boolean;

-- name: SetGarageVisibility :exec
INSERT INTO garage_tayfa_visibility (user_id, tayfa_id, visible, updated_at)
VALUES (@user_id, @tayfa_id, @visible, @now)
ON CONFLICT (user_id, tayfa_id) DO UPDATE SET visible = EXCLUDED.visible, updated_at = EXCLUDED.updated_at;

-- Araçlar bileşeni: sahibinin garajdan seçtiği araç (kopyalanacak alanlar).
-- name: GetOwnGarageVehicleForEvent :one
SELECT make, model, model_year, color FROM garage_vehicles
WHERE owner_id = @owner_id AND id = @id AND deleted_at IS NULL;
