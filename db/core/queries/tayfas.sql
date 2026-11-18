-- Tayfa, kurucu üyelik, modüller ve ilgi alanları tek transaction'da
-- yazılır; birleştirmeyi app katmanı yapar.
-- name: CreateTayfa :exec
INSERT INTO tayfas (id, name, name_normalized, description, join_code, join_policy, created_by, client_request_id)
VALUES (@id, @name, @name_normalized, @description, @join_code, @join_policy, @created_by, @client_request_id);

-- name: GetTayfa :one
SELECT id, name, name_normalized, description, join_code, status, visibility,
       join_policy, city, district, created_by, last_activity_at, suspended_at,
       deletion_requested_at, deletion_scheduled_for, members_can_create_events,
       created_at, updated_at, cover_upload_id, kind, converted_at
FROM tayfas
WHERE id = @id AND deleted_at IS NULL;

-- Tekrar gönderilen oluşturma isteği aynı tayfayı döndürür.
-- name: GetTayfaByClientRequest :one
SELECT id FROM tayfas
WHERE created_by = @created_by AND client_request_id = @client_request_id;

-- name: GetTayfaByJoinCode :one
SELECT id, join_policy, status, suspended_at, kind
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
-- açıp silerek sınır aşılamaz. Anlık tayfa sayılmaz; kalıcıya dönüştürülen
-- dönüşüm anında açılmış sayılır (ADR-0032 §5, §7).
-- name: CountTayfasCreatedSince :one
SELECT count(*) FROM tayfas
WHERE created_by = @created_by AND kind = 'standard'
  AND COALESCE(converted_at, created_at) > @since;

-- İlgi alanı katalog anahtarıdır (catalog_interests, ADR-0025 §2).
-- name: AddTayfaInterest :exec
INSERT INTO tayfa_interests (tayfa_id, interest_key)
VALUES (@tayfa_id, @interest_key)
ON CONFLICT DO NOTHING;

-- name: DeleteTayfaInterests :exec
DELETE FROM tayfa_interests WHERE tayfa_id = @tayfa_id;

-- Katalog sırasıyla.
-- name: ListTayfaInterests :many
SELECT ti.interest_key
FROM tayfa_interests ti
JOIN catalog_interests ci ON ci.key = ti.interest_key
WHERE ti.tayfa_id = @tayfa_id
ORDER BY ci.position, ci.key;

-- İlgi alanı doğrulaması için: anahtar ve durum (emekliler dahil).
-- name: ListCatalogInterestStatuses :many
SELECT key, status FROM catalog_interests;

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
       deletion_requested_at, deletion_scheduled_for, members_can_create_events,
       created_at, updated_at, cover_upload_id, kind, converted_at
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
    description = @description, join_policy = @join_policy,
    members_can_create_events = @members_can_create_events
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
       t.join_policy, t.suspended_at, t.deletion_scheduled_for,
       t.members_can_create_events, t.created_at, t.updated_at, t.cover_upload_id, t.kind,
       t.converted_at, m.role, m.status AS membership_status, m.muted_until,
       m.created_at AS membership_created_at
FROM memberships m
JOIN tayfas t ON t.id = m.tayfa_id
WHERE m.user_id = @user_id
  AND m.status IN ('active', 'pending')
  AND t.deleted_at IS NULL
  AND (m.created_at, m.tayfa_id) < (@before_created_at::timestamptz, @before_tayfa_id::uuid)
ORDER BY m.created_at DESC, m.tayfa_id DESC
LIMIT @page_size;

-- Her tayfanın ilgi alanları katalog sırasıyla.
-- name: ListInterestsForTayfas :many
SELECT ti.tayfa_id, ti.interest_key
FROM tayfa_interests ti
JOIN catalog_interests ci ON ci.key = ti.interest_key
WHERE ti.tayfa_id = ANY(@tayfa_ids::uuid[])
ORDER BY ti.tayfa_id, ci.position, ci.key;

-- name: ListModulesForTayfas :many
SELECT tayfa_id, module, enabled FROM tayfa_modules
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]);

-- name: CountActiveMembersForTayfas :many
SELECT tayfa_id, count(*) AS member_count FROM memberships
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]) AND status = 'active'
GROUP BY tayfa_id;

-- Onay bekleyen katılım istekleri (issue #150); memberships_pending_idx.
-- name: CountPendingMembersForTayfas :many
SELECT tayfa_id, count(*) AS pending_count FROM memberships
WHERE tayfa_id = ANY(@tayfa_ids::uuid[]) AND status = 'pending'
GROUP BY tayfa_id;

-- Kapalı ya da kaydı olmayan modül kapalı sayılır (varsayılan "hayır").
-- name: IsTayfaModuleEnabled :one
SELECT EXISTS (
  SELECT 1 FROM tayfa_modules
  WHERE tayfa_id = @tayfa_id AND module = @module AND enabled
)::boolean AS enabled;

-- Tayfa aktivitesi (pasif tayfa temizliğinin dayanağı, v14 "Tayfa
-- yönetimi"). Yalnızca ileri gider; not_after = at - 1 saat verilir:
-- sıcak satıra her yazmada yeni sürüm açılmaz, temizlik işi gün ölçeğinde
-- çalışır.
-- name: TouchTayfaActivity :execrows
UPDATE tayfas SET last_activity_at = @at
WHERE id = @id AND deleted_at IS NULL AND last_activity_at <= @not_after;

-- ---------------------------------------------------------------------------
-- Anlık etkinlik (ADR-0032)
-- ---------------------------------------------------------------------------

-- Anlık tayfa: gizli, onaylı katılım; adı etkinlik başlığıdır.
-- name: CreateInstantTayfa :exec
INSERT INTO tayfas (id, name, name_normalized, join_code, join_policy, kind, created_by, client_request_id)
VALUES (@id, @name, @name_normalized, @join_code, 'approval', 'instant', @created_by, @client_request_id);

-- Kullanıcının organizatörü olduğu açık anlık etkinlikler: silinmemiş,
-- arşivlenmemiş anlık tayfada bitmemiş ve iptal edilmemiş etkinlik.
-- name: CountOpenInstantEvents :one
SELECT count(*) FROM tayfas t
JOIN events e ON e.tayfa_id = t.id
WHERE t.created_by = @created_by AND t.kind = 'instant'
  AND t.deleted_at IS NULL AND t.status <> 'archived'
  AND e.deleted_at IS NULL AND e.status = 'scheduled' AND e.ends_at > @now;

-- Kullanıcının son @since'ten beri açtığı anlık tayfalar: iptal edilen,
-- silinen ve dönüştürülenler de sayılır (günde 5; ADR-0032 §5).
-- name: CountInstantTayfasCreatedSince :one
SELECT count(*) FROM tayfas
WHERE created_by = @created_by AND (kind = 'instant' OR converted_at IS NOT NULL)
  AND created_at > @since;

-- Anlık tayfanın tek etkinliği (silinmişler dahil en fazla bir satır).
-- name: ListInstantEventIDs :many
SELECT e.tayfa_id, e.id FROM events e
JOIN tayfas t ON t.id = e.tayfa_id
WHERE e.tayfa_id = ANY(@tayfa_ids::uuid[]) AND t.kind = 'instant' AND e.deleted_at IS NULL;

-- Etkinlik başlığı değişince anlık tayfanın adı da değişir.
-- name: SetInstantTayfaName :execrows
UPDATE tayfas SET name = @name, name_normalized = @name_normalized
WHERE id = @id AND kind = 'instant' AND deleted_at IS NULL;

-- "Bu ekiple tayfa kur": tür, ad ve ayarlar; hareketsizlik saati sıfırlanır.
-- name: ConvertInstantTayfa :execrows
UPDATE tayfas
SET kind = 'standard', converted_at = @converted_at, name = @name,
    name_normalized = @name_normalized, description = @description,
    join_policy = @join_policy, last_activity_at = @converted_at,
    -- Organizatör hesabını sildiyse devralan yönetici dönüştürür; tayfa
    -- onun günlük açma sınırına sayılır (ADR-0032 §7).
    created_by = COALESCE(created_by, @converted_by::uuid)
WHERE id = @id AND kind = 'instant' AND deleted_at IS NULL;
