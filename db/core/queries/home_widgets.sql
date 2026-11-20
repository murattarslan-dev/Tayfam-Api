-- Tayfa ana sayfa bileşenleri (issue #268; ADR-0027 §2–§4). Bileşen
-- listesi koddadır (internal/core/app/home); kodun tanımadığı satır
-- okunurken atlanır.

-- name: ListHomeWidgets :many
SELECT widget_key, enabled, position, config
FROM tayfa_home_widgets
WHERE tayfa_id = @tayfa_id
ORDER BY position, widget_key;

-- Varsayılan bileşen: satır yoksa açık olarak, en sona eklenir; var olan
-- satıra (yöneticinin kapattığı bileşene) dokunulmaz.
-- name: AddHomeWidgetDefault :exec
INSERT INTO tayfa_home_widgets (tayfa_id, widget_key, enabled, position, updated_at)
SELECT @tayfa_id::uuid, @widget_key::text, true, COALESCE(MAX(w.position) + 1, 0), @now::timestamptz
FROM tayfa_home_widgets w
WHERE w.tayfa_id = @tayfa_id::uuid
ON CONFLICT (tayfa_id, widget_key) DO NOTHING;

-- Yöneticinin düzeni: bütün liste, bileşen başına bir çağrı.
-- name: SetHomeWidget :exec
INSERT INTO tayfa_home_widgets (tayfa_id, widget_key, enabled, position, config, updated_at)
VALUES (@tayfa_id, @widget_key, @enabled, @position, @config, @now)
ON CONFLICT (tayfa_id, widget_key) DO UPDATE
SET enabled = EXCLUDED.enabled, position = EXCLUDED.position,
    config = EXCLUDED.config, updated_at = EXCLUDED.updated_at;

-- İlgi alanlarının varsayılan bileşenleri, katalog sırasıyla.
-- name: ListInterestDefaultWidgets :many
SELECT key, default_widgets
FROM catalog_interests
WHERE key = ANY(@keys::text[])
ORDER BY position, key;

-- Şampiyonlar kartı (ADR-0027 §4): tayfanın ilan edilmiş kutlamaları,
-- başlamış, iptal edilmemiş ve silinmemiş etkinliklerde; en yeni ilan
-- önce. year 0 ise yıl süzgeci yoktur; event_types boşsa her tür.
-- name: ListHomeChampionCelebrations :many
SELECT e.id, e.title, e.type, e.type_version, e.starts_at, c.component_key, c.announced_at
FROM event_celebrations c
JOIN events e ON e.id = c.event_id
WHERE e.tayfa_id = @tayfa_id
  AND e.deleted_at IS NULL
  AND e.cancelled_at IS NULL
  AND e.starts_at >= @since
  AND e.starts_at <= @now
  AND (@year::integer = 0
       OR EXTRACT(YEAR FROM e.starts_at AT TIME ZONE 'Europe/Istanbul')::integer = @year::integer)
  AND (cardinality(@event_types::text[]) = 0 OR e.type = ANY(@event_types::text[]))
ORDER BY c.announced_at DESC, e.id, c.component_key
LIMIT @max_rows;
