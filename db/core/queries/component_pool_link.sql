-- Soru havuzu bağlantısı bileşeni (`pool_link`; issue #271; ADR-0037 §3,
-- ADR-0040 §2). Etkinlik her zaman önce tayfa kapsamında yüklenir ve
-- bileşen etkinliğin tür sürümünde çözülür (internal/core/app/component:
-- View / Change); sorular etkinliğin tayfasıyla sınırlıdır (IDOR). Yetki app
-- katmanındadır (internal/core/app/component/poollink).

-- Bağlı sorular, organizatörün sırasıyla. Silinmiş (gizlenmiş) soru
-- listede görünmez; bağı kalır, geri alınırsa yeniden görünür.
-- name: ListEventPoolLinks :many
SELECT q.id, q.author_id, q.body, q.subject_key, q.topic_key, q.photo1_upload_id, q.photo2_upload_id,
       q.created_at,
       (SELECT count(*) FROM answers a WHERE a.question_id = q.id AND a.deleted_at IS NULL)::integer AS answer_count
FROM event_pool_links l
JOIN questions q ON q.id = l.question_id AND q.tayfa_id = @tayfa_id AND q.pool = 'question_pool'
WHERE l.event_id = @event_id AND l.component_key = @component_key AND q.deleted_at IS NULL
ORDER BY l.position;

-- Bütün bağlar (silinmiş sorular dahil), sırasıyla: aynı listenin tekrar
-- yazılması bir şey değiştirmez.
-- name: ListEventPoolLinkIDs :many
SELECT question_id FROM event_pool_links
WHERE event_id = @event_id AND component_key = @component_key
ORDER BY position;

-- Bağlanabilecek sorular: tayfanın havuzundaki görünür sorular.
-- name: CountVisiblePoolQuestions :one
SELECT count(*)::integer FROM questions
WHERE tayfa_id = @tayfa_id AND pool = 'question_pool' AND deleted_at IS NULL AND id = ANY(@ids::uuid[]);

-- name: DeleteEventPoolLinks :execrows
DELETE FROM event_pool_links WHERE event_id = @event_id AND component_key = @component_key;

-- name: AddEventPoolLink :exec
INSERT INTO event_pool_links (event_id, component_key, question_id, position)
VALUES (@event_id, @component_key, @question_id, @position);
