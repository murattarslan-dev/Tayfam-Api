-- Tayfa sözlüğü (issue #244; ADR-0037 §4). Her sorgu tayfa ve ad alanı
-- kapsamında çalışır (IDOR); üyelik ve yetki app katmanındadır
-- (internal/core/app/vocabulary).

-- name: FindVocabularyValue :one
SELECT id FROM tayfa_vocabulary
WHERE tayfa_id = @tayfa_id AND namespace = @namespace AND value_normalized = @value_normalized;

-- Aynı değer eşzamanlı eklenirse satır dönmez; çağıran yeniden arar.
-- name: CreateVocabularyValue :one
INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized, created_by, created_at)
VALUES (@id, @tayfa_id, @namespace, @value, @value_normalized, @created_by, @created_at)
ON CONFLICT (tayfa_id, namespace, value_normalized) DO NOTHING
RETURNING id;

-- name: GetVocabularyValue :one
SELECT id, value, merged_into FROM tayfa_vocabulary
WHERE tayfa_id = @tayfa_id AND namespace = @namespace AND id = @id;

-- name: GetVocabularyValueForUpdate :one
SELECT id, value, merged_into FROM tayfa_vocabulary
WHERE tayfa_id = @tayfa_id AND namespace = @namespace AND id = @id
FOR UPDATE;

-- Birleştirilmemiş değerler; @query (normalize) kendisinde ya da ona
-- birleştirilmiş bir değerde geçenler. Önce @query ile başlayanlar.
-- name: SearchVocabulary :many
SELECT t.id, t.value
FROM tayfa_vocabulary t
WHERE t.tayfa_id = @tayfa_id AND t.namespace = @namespace AND t.merged_into IS NULL
  AND (@query::text = '' OR EXISTS (
    SELECT 1 FROM tayfa_vocabulary a
    WHERE a.tayfa_id = t.tayfa_id AND a.namespace = t.namespace
      AND (a.id = t.id OR a.merged_into = t.id)
      AND strpos(a.value_normalized, @query::text) > 0
  ))
ORDER BY strpos(t.value_normalized, @query::text) = 1 DESC, t.value_normalized, t.id
LIMIT @n;

-- Birleştirilmiş değerler ve hedefleri (geri alma için).
-- name: SearchMergedVocabulary :many
SELECT s.id, s.value, t.id AS target_id, t.value AS target_value
FROM tayfa_vocabulary s
JOIN tayfa_vocabulary t ON t.id = s.merged_into
WHERE s.tayfa_id = @tayfa_id AND s.namespace = @namespace
  AND (@query::text = '' OR strpos(s.value_normalized, @query::text) > 0 OR strpos(t.value_normalized, @query::text) > 0)
ORDER BY s.value_normalized, s.id
LIMIT @n;

-- Birleştirilen değere bağlı değerler hedefe taşınır (zincir yok).
-- name: MoveVocabularyMerges :exec
UPDATE tayfa_vocabulary SET merged_into = @target_id
WHERE tayfa_id = @tayfa_id AND namespace = @namespace AND merged_into = @source_id;

-- name: SetVocabularyMergedInto :execrows
UPDATE tayfa_vocabulary SET merged_into = sqlc.narg('merged_into')
WHERE tayfa_id = @tayfa_id AND namespace = @namespace AND id = @id;

-- Değere (ve ona birleştirilmiş değerlere) bağlı silinmemiş günlük
-- kayıtlarının etkinlik yılları (Europe/Istanbul; istatistik, ADR-0026 §2).
-- name: VocabularyEntryYears :many
SELECT DISTINCT extract(year FROM e.starts_at AT TIME ZONE 'Europe/Istanbul')::int AS year
FROM event_log_entry_vocabulary x
JOIN event_log_entries l ON l.id = x.entry_id
JOIN events e ON e.id = l.event_id
WHERE e.tayfa_id = @tayfa_id AND l.deleted_at IS NULL
  AND x.vocabulary_id IN (
    SELECT v.id FROM tayfa_vocabulary v
    WHERE v.tayfa_id = @tayfa_id AND v.namespace = @namespace AND (v.id = @id OR v.merged_into = @id)
  )
ORDER BY year;

-- name: CreateLogEntryVocabulary :exec
INSERT INTO event_log_entry_vocabulary (entry_id, field_key, vocabulary_id)
VALUES (@entry_id, @field_key, @vocabulary_id);

-- Kayıtların sözlüğe bağlı alanları, birleştirme hedefine çözülmüş olarak.
-- Kayıtlar çağıranın etkinlik ve bileşen kapsamında okunmuştur.
-- name: ListLogEntryVocabulary :many
SELECT x.entry_id, x.field_key, r.id AS vocabulary_id, r.value
FROM event_log_entry_vocabulary x
JOIN tayfa_vocabulary s ON s.id = x.vocabulary_id
JOIN tayfa_vocabulary r ON r.id = COALESCE(s.merged_into, s.id)
WHERE x.entry_id = ANY(@entry_ids::uuid[]);
