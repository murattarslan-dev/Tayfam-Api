-- Etkinlik türü kataloğu (issue #237; ADR-0025 §2, §8). core_app bu
-- tabloları yalnızca okur; katalog migration'la (ileride platform paneliyle)
-- değişir.

-- Kataloğun bütününün sürümü; GET /v1/catalog'un ETag'i.
-- name: GetCatalogVersion :one
SELECT catalog_version FROM catalog_meta;

-- Emekliye ayrılanlar dahil: eski tayfalar ve profiller onları gösterir.
-- name: ListCatalogInterests :many
SELECT key, position, labels, icon, status, default_widgets
FROM catalog_interests
ORDER BY position, key;

-- Aktif türlerin güncel tanımları.
-- name: ListActiveCatalogEventTypes :many
SELECT key, interest_keys, labels, icon, modes, version, definition
FROM catalog_event_types
WHERE status = 'active'
ORDER BY position, key;

-- Bir türün bir sürümü; eski etkinlikler için (emekli türler dahil).
-- İlgi alanları ve durum türün güncel halinden gelir.
-- name: GetCatalogEventTypeVersion :one
SELECT v.type_key, v.version, v.labels, v.icon, v.modes, v.definition, t.interest_keys, t.status
FROM catalog_event_type_versions v
JOIN catalog_event_types t ON t.key = v.type_key
WHERE v.type_key = @type_key AND v.version = @version;
