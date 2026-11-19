-- Liste bileşeni (`checklist`; issue #241; ADR-0025 §4). Etkinlik her zaman
-- önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür sürümünde çözülür
-- (internal/core/app/component: View / Change); buradaki sorgular o
-- etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/checklist).

-- name: ListChecklistItems :many
SELECT id, name, quantity, assigned_user_id FROM event_checklist_items
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY created_at, id;

-- name: CountChecklistItems :one
SELECT count(*) FROM event_checklist_items
WHERE event_id = @event_id AND component_key = @component_key;

-- name: GetChecklistItem :one
SELECT assigned_user_id FROM event_checklist_items
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- name: CreateChecklistItem :exec
INSERT INTO event_checklist_items (id, event_id, component_key, name, quantity, assigned_user_id, created_by, created_at, updated_at)
VALUES (@id, @event_id, @component_key, @name, @quantity, @assigned_user_id, @created_by, @created_at, @created_at);

-- name: SetChecklistItemAssignee :execrows
UPDATE event_checklist_items SET assigned_user_id = @assigned_user_id, updated_at = @updated_at
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;

-- name: DeleteChecklistItem :execrows
DELETE FROM event_checklist_items
WHERE event_id = @event_id AND component_key = @component_key AND id = @id;
