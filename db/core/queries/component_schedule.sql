-- Program akışı bileşeni (`schedule`; issue #247; ADR-0025 §4). Etkinlik her
-- zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde
-- çözülür (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/schedule).

-- name: ListScheduleItems :many
SELECT id, starts_at, ends_at, title, note FROM event_schedule_items
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY starts_at, created_at, id;

-- name: CountScheduleItems :one
SELECT count(*) FROM event_schedule_items
WHERE event_id = @event_id AND component_key = @component_key;

-- name: CreateScheduleItem :exec
INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, ends_at, title, note, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @starts_at, @ends_at, @title, @note, @created_at, @created_at);

-- name: UpdateScheduleItem :execrows
UPDATE event_schedule_items
SET starts_at = @starts_at, ends_at = @ends_at, title = @title, note = @note, updated_at = @updated_at
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- name: DeleteScheduleItem :execrows
DELETE FROM event_schedule_items
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- Seri örneği (issue #247): şablonun maddeleri yeni kimliklerle, saatleri
-- örneğin başlangıcına kaydırılarak. Kopya ortak tetikleyiciden geçer.
-- name: CopyScheduleItems :exec
INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, ends_at, title, note)
SELECT uuidv7(), dst.id, src.component_key, src.starts_at + (dst.starts_at - tmpl.starts_at),
  src.ends_at + (dst.starts_at - tmpl.starts_at), src.title, src.note
FROM event_schedule_items AS src
JOIN events AS tmpl ON tmpl.id = src.event_id
JOIN events AS dst ON dst.id = @to_event_id
WHERE src.event_id = @from_event_id
ORDER BY src.starts_at, src.created_at, src.id;
