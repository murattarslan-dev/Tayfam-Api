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

-- Bir türün güncel hali; yeni etkinlik bu sürümle oluşturulur. Emekli türü
-- app katmanı reddeder.
-- name: GetCatalogEventType :one
SELECT key, interest_keys, labels, icon, modes, version, definition, status
FROM catalog_event_types
WHERE key = @key;

-- Ders ve konu etiketleri (issue #259; ADR-0040 §5), emekliler dahil: eski
-- içerik onları göstermeye devam eder. Düzeylerin parent_key'i ''.
-- name: ListCatalogStudyTags :many
SELECT key, COALESCE(parent_key, '')::text AS parent_key, position, labels, status
FROM catalog_study_tags
ORDER BY position, key;

-- Bir etiketin yeri ve yeni içerikte seçilip seçilemeyeceği: kendisi ve
-- üst etiketleri aktifse seçilebilir. depth 1 düzey, 2 ders, 3 konu.
-- name: GetCatalogStudyTag :one
SELECT t.key, COALESCE(t.parent_key, '')::text AS parent_key,
  (CASE WHEN t.parent_key IS NULL THEN 1 WHEN p.parent_key IS NULL THEN 2 ELSE 3 END)::integer AS depth,
  (t.status = 'active' AND COALESCE(p.status, 'active') = 'active'
    AND COALESCE(g.status, 'active') = 'active')::boolean AS selectable
FROM catalog_study_tags t
LEFT JOIN catalog_study_tags p ON p.key = t.parent_key
LEFT JOIN catalog_study_tags g ON g.key = p.parent_key
WHERE t.key = @key;

-- Son yayınlanan catalog.changed olayının katalog sürümü; hiç yoksa 0
-- (issue #280, ADR-0042; catalog-publish işi). domain_events_type_id_idx
-- ile tek satır okunur.
-- name: GetLastPublishedCatalogVersion :one
SELECT COALESCE((
  SELECT (e.payload ->> 'catalog_version')::bigint
  FROM domain_events e
  WHERE e.type = 'catalog.changed'
  ORDER BY e.id DESC
  LIMIT 1
), 0)::bigint AS catalog_version;
