-- Günün içeriği (issue #269; ADR-0027 §5). Tayfa her zaman önce üyelik
-- kontrolüyle yüklenir (internal/core/app/daily); buradaki sorgular o
-- tayfanın kimliğiyle çalışır (IDOR). Yetki, modül ve seçim kuralı app
-- katmanındadır.

-- name: ListDailyItems :many
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND deleted_at IS NULL
ORDER BY position, id;

-- Geri alınabilecek silinmişler (son 30 gün), son silinen önce.
-- name: ListDeletedDailyItems :many
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND deleted_at IS NOT NULL AND deleted_at > @since
ORDER BY deleted_at DESC, id;

-- name: GetDailyItem :one
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: GetDailyItemForUpdate :one
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- name: GetDailyItemByClientRequest :one
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND client_request_id = @client_request_id;

-- name: CountDailyItems :one
SELECT count(*) FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND deleted_at IS NULL;

-- Yeni ya da geri alınan içerik havuzun sonuna gider; silinmişler de
-- sayılır, böylece iki içerik aynı yeri almaz.
-- name: NextDailyPosition :one
SELECT (COALESCE(max(position), -1) + 1)::integer AS next_position
FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id;

-- name: CreateDailyItem :exec
INSERT INTO tayfa_daily_items (
  id, tayfa_id, body, attribution, position, pin_month, pin_day, pin_year,
  created_by, client_request_id, created_at, updated_at)
VALUES (
  @id, @tayfa_id, @body, sqlc.narg(attribution), @position, sqlc.narg(pin_month), sqlc.narg(pin_day), sqlc.narg(pin_year),
  @created_by, @client_request_id, @now, @now);

-- name: UpdateDailyItem :execrows
UPDATE tayfa_daily_items
SET body = @body, attribution = sqlc.narg(attribution),
    pin_month = sqlc.narg(pin_month), pin_day = sqlc.narg(pin_day), pin_year = sqlc.narg(pin_year),
    updated_at = @now
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Sıra bütün liste olarak yazılır: ids'teki n'inci içerik n-1'inci yere.
-- name: ReorderDailyItems :execrows
UPDATE tayfa_daily_items AS i
SET position = (o.ord - 1)::integer
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS o(item_id, ord)
WHERE i.tayfa_id = @tayfa_id AND i.id = o.item_id AND i.deleted_at IS NULL;

-- name: DeleteDailyItem :execrows
UPDATE tayfa_daily_items
SET deleted_at = @now, deleted_by = @deleted_by
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: RestoreDailyItem :execrows
UPDATE tayfa_daily_items
SET deleted_at = NULL, deleted_by = NULL, position = @position, updated_at = @now
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NOT NULL;

-- Aynı güne başka canlı sabitleme (ADR-0027 §5 "Çakışma uyarısı"): yılı
-- olmayan sabitleme her yılın o günüyle çakışır.
-- name: CountDailyPinConflicts :one
SELECT count(*) FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND id <> @id AND deleted_at IS NULL
  AND pin_month = @pin_month AND pin_day = @pin_day
  AND (pin_year IS NULL OR sqlc.narg(pin_year)::smallint IS NULL OR pin_year = sqlc.narg(pin_year)::smallint);

-- Günün sabitlemesi: o güne (yılı o yıl ya da yılsız) sabitlenmiş canlı
-- içeriklerden sırada önce gelen.
-- name: GetPinnedDailyItem :one
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND deleted_at IS NULL
  AND pin_month = @pin_month AND pin_day = @pin_day
  AND (pin_year IS NULL OR pin_year = @pin_year)
ORDER BY position, id
LIMIT 1;

-- Sıradaki: from_position ya da sonrasındaki ilk sabitlenmemiş canlı içerik.
-- name: NextDailyRotationItem :one
SELECT * FROM tayfa_daily_items
WHERE tayfa_id = @tayfa_id AND deleted_at IS NULL AND pin_month IS NULL
  AND position >= @from_position
ORDER BY position, id
LIMIT 1;

-- name: EnsureDailyState :exec
INSERT INTO tayfa_daily_state (tayfa_id, updated_at) VALUES (@tayfa_id, @now)
ON CONFLICT (tayfa_id) DO NOTHING;

-- name: GetDailyState :one
SELECT next_position, shown_on, shown_item_id FROM tayfa_daily_state
WHERE tayfa_id = @tayfa_id;

-- Okunan durum hâlâ aynıysa ilerletir (karşılaştır-yaz): aynı anda gelen
-- iki ilk istekten yalnızca biri yazar, diğeri yeniden okur.
-- name: AdvanceDailyState :execrows
UPDATE tayfa_daily_state
SET next_position = @next_position, shown_on = @shown_on, shown_item_id = @shown_item_id, updated_at = @now
WHERE tayfa_id = @tayfa_id
  AND next_position = @old_next_position
  AND shown_on IS NOT DISTINCT FROM sqlc.narg(old_shown_on)::date
  AND shown_item_id IS NOT DISTINCT FROM sqlc.narg(old_shown_item_id)::uuid;
