-- Platform panelinde katalog yönetimi (issue #262; ADR-0025 §7, ADR-0039
-- §1, ADR-0040 §3 ve §5). Yalnızca internal/core/app/admin çağırır; her
-- yazma, katalog satırı kilitlenmiş bir transaction'da, doğrulamadan sonra
-- yapılır. Anahtar değişmez, satır silinmez (engelli alan adı listesi
-- hariç); yetkiler 00067'de.

-- Panel kayıtları sırayla yazılır: ders/konu listesinin "Diğer" kuralı ve
-- rozetlerin tür denetimi kataloğun bütününe bakar.
-- name: AdminLockCatalog :one
SELECT catalog_version FROM catalog_meta FOR UPDATE;

-- name: AdminBumpCatalogVersion :one
UPDATE catalog_meta SET catalog_version = catalog_version + 1
RETURNING catalog_version;

-- Emekliler dahil bütün türlerin güncel hali.
-- name: AdminListCatalogEventTypes :many
SELECT key, interest_keys, position, labels, icon, modes, status, version, definition
FROM catalog_event_types
ORDER BY position, key;

-- name: AdminListScorePresets :many
SELECT key, position, labels, params, status
FROM catalog_score_presets
ORDER BY position, key;

-- name: AdminListBlockedDomains :many
SELECT domain, created_at, initial
FROM blocked_domains
ORDER BY domain;

-- İlgi alanları.

-- name: AdminGetCatalogInterestForUpdate :one
SELECT key, position, labels, icon, status, default_widgets
FROM catalog_interests
WHERE key = @key
FOR UPDATE;

-- name: AdminInsertCatalogInterest :exec
INSERT INTO catalog_interests (key, position, labels, icon, status, default_widgets)
VALUES (@key, @position, @labels, @icon, @status, @default_widgets::text[]);

-- name: AdminUpdateCatalogInterest :exec
UPDATE catalog_interests
SET position = @position, labels = @labels, icon = @icon, status = @status,
    default_widgets = @default_widgets::text[]
WHERE key = @key;

-- Türler. Sürüm satırı yalnızca eklenir.

-- name: AdminGetCatalogEventTypeForUpdate :one
SELECT key, interest_keys, position, labels, icon, modes, status, version, definition
FROM catalog_event_types
WHERE key = @key
FOR UPDATE;

-- name: AdminInsertCatalogEventType :exec
INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, status, version, definition)
VALUES (@key, @interest_keys::text[], @position, @labels, @icon, @modes::text[], @status, @version, @definition);

-- name: AdminUpdateCatalogEventType :exec
UPDATE catalog_event_types
SET interest_keys = @interest_keys::text[], position = @position, labels = @labels, icon = @icon,
    modes = @modes::text[], status = @status, version = @version, definition = @definition
WHERE key = @key;

-- name: AdminInsertCatalogEventTypeVersion :exec
INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
VALUES (@type_key, @version, @labels, @icon, @modes::text[], @definition);

-- Bir türün ilgi alanlarının durumu (yeni eklenen ilgi alanı aktif olmalı).
-- name: AdminListCatalogInterestStatuses :many
SELECT key, status FROM catalog_interests
WHERE key = ANY(@keys::text[]);

-- Yazboz bileşeninin sunduğu ön ayarlar katalogda olmalı.
-- name: AdminListScorePresetKeys :many
SELECT key FROM catalog_score_presets
WHERE key = ANY(@keys::text[]);

-- Rozetler.

-- name: AdminGetCatalogBadgeForUpdate :one
SELECT key, scope, position, labels, icon, rule, status
FROM catalog_badges
WHERE key = @key
FOR UPDATE;

-- name: AdminInsertCatalogBadge :exec
INSERT INTO catalog_badges (key, scope, position, labels, icon, rule, status)
VALUES (@key, @scope, @position, @labels, @icon, @rule, @status);

-- name: AdminUpdateCatalogBadge :exec
UPDATE catalog_badges
SET position = @position, labels = @labels, icon = @icon, rule = @rule, status = @status
WHERE key = @key;

-- Ders ve konu etiketleri. Üst etiket değişmez (00052'deki tetikleyici de
-- korur).

-- name: AdminGetCatalogStudyTagForUpdate :one
SELECT key, COALESCE(parent_key, '')::text AS parent_key, position, labels, status
FROM catalog_study_tags
WHERE key = @key
FOR UPDATE;

-- name: AdminInsertCatalogStudyTag :exec
INSERT INTO catalog_study_tags (key, parent_key, position, labels, status)
VALUES (@key, sqlc.narg('parent_key'), @position, @labels, @status);

-- name: AdminUpdateCatalogStudyTag :exec
UPDATE catalog_study_tags
SET position = @position, labels = @labels, status = @status
WHERE key = @key;

-- Yazboz ön ayarları.

-- name: AdminGetScorePresetForUpdate :one
SELECT key, position, labels, params, status
FROM catalog_score_presets
WHERE key = @key
FOR UPDATE;

-- name: AdminInsertScorePreset :exec
INSERT INTO catalog_score_presets (key, position, labels, params, status)
VALUES (@key, @position, @labels, @params, @status);

-- name: AdminUpdateScorePreset :exec
UPDATE catalog_score_presets
SET position = @position, labels = @labels, params = @params, status = @status
WHERE key = @key;

-- Engelli alan adları: ekleyen platform yöneticisi created_by'da.

-- name: AdminInsertBlockedDomain :execrows
INSERT INTO blocked_domains (domain, created_by)
VALUES (@domain, @created_by)
ON CONFLICT (domain) DO NOTHING;

-- name: AdminDeleteBlockedDomain :execrows
DELETE FROM blocked_domains WHERE domain = @domain;
