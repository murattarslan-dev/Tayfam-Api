-- Soru-cevap (issue #59). Her sorgu tayfa_id ile sınırlıdır (IDOR); üyelik
-- ve izin kontrolü app katmanındadır (access, authz).

-- name: CreateQuestion :exec
INSERT INTO questions (id, tayfa_id, author_id, title, body, client_request_id)
VALUES (@id, @tayfa_id, @author_id, @title, @body, @client_request_id);

-- Tekrar gönderilen soru isteği aynı soruyu döndürür.
-- name: GetQuestionByClientRequest :one
SELECT id, tayfa_id, deleted_at
FROM questions
WHERE author_id = @author_id AND client_request_id = @client_request_id;

-- name: GetQuestion :one
SELECT q.id, q.tayfa_id, q.author_id, q.title, q.body, q.created_at, q.updated_at,
       (SELECT count(*) FROM answers a WHERE a.question_id = q.id AND a.deleted_at IS NULL)::integer AS answer_count
FROM questions q
WHERE q.tayfa_id = @tayfa_id AND q.id = @id AND q.deleted_at IS NULL;

-- Yazma işlemleri satırı kilitleyerek okur; silinmiş satır da döner.
-- name: GetQuestionForUpdate :one
SELECT id, tayfa_id, author_id, title, body, deleted_at, deleted_by
FROM questions
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- Yeniden eskiye. İsteğe bağlı etiket filtresi. Sayfa, önceki sayfanın son
-- satırından sonra başlar (created_at, id azalan).
-- name: ListQuestionsPage :many
SELECT q.id, q.tayfa_id, q.author_id, q.title, q.body, q.created_at, q.updated_at,
       (SELECT count(*) FROM answers a WHERE a.question_id = q.id AND a.deleted_at IS NULL)::integer AS answer_count
FROM questions q
WHERE q.tayfa_id = @tayfa_id
  AND q.deleted_at IS NULL
  AND (NOT @has_tag::boolean
       OR EXISTS (SELECT 1 FROM question_tags qt WHERE qt.question_id = q.id AND qt.tayfa_id = @tayfa_id AND qt.tag_id = @tag_id::uuid))
  AND (NOT @has_cursor::boolean
       OR (q.created_at, q.id) < (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY q.created_at DESC, q.id DESC
LIMIT @page_size;

-- Türkçe tam metin arama. Sorgu metni tr_lower ile, belgelerle aynı
-- tabloya göre küçültülür; websearch_to_tsquery her girdiyi kabul eder.
-- Sıra: ilgi azalan, eşitlikte yeniden eskiye.
-- name: SearchQuestionsPage :many
WITH s AS (
  SELECT q.id, q.tayfa_id, q.author_id, q.title, q.body, q.created_at, q.updated_at,
         ts_rank(q.search_vector, websearch_to_tsquery('turkish'::regconfig, tr_lower(@query::text)))::real AS rank
  FROM questions q
  WHERE q.tayfa_id = @tayfa_id
    AND q.deleted_at IS NULL
    AND q.search_vector @@ websearch_to_tsquery('turkish'::regconfig, tr_lower(@query::text))
    AND (NOT @has_tag::boolean
         OR EXISTS (SELECT 1 FROM question_tags qt WHERE qt.question_id = q.id AND qt.tayfa_id = @tayfa_id AND qt.tag_id = @tag_id::uuid))
)
SELECT s.id, s.tayfa_id, s.author_id, s.title, s.body, s.created_at, s.updated_at, s.rank,
       (SELECT count(*) FROM answers a WHERE a.question_id = s.id AND a.deleted_at IS NULL)::integer AS answer_count
FROM s
WHERE NOT @has_cursor::boolean
   OR (s.rank, s.id) < (@after_rank::real, @after_id::uuid)
ORDER BY s.rank DESC, s.id DESC
LIMIT @page_size;

-- name: UpdateQuestionContent :execrows
UPDATE questions
SET title = @title, body = @body
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Moderatör/yönetici silmesi: içerik gizlenir, geri alınabilir.
-- name: HideQuestion :execrows
UPDATE questions
SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Yazarın kendi silmesi: başlık ve gövde hemen boşaltılır, geri alınamaz
-- (v14 "Silinen içeriği geri alma"). Etiket bağları ayrıca silinir.
-- name: EraseQuestion :execrows
UPDATE questions
SET deleted_at = @deleted_at, deleted_by = @deleted_by, title = '', body = ''
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: RestoreQuestion :execrows
UPDATE questions
SET deleted_at = NULL, deleted_by = NULL
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NOT NULL AND char_length(title) > 0;

-- Etiketler.

-- name: CountTags :one
SELECT count(*)::integer FROM tags WHERE tayfa_id = @tayfa_id;

-- name: GetTagsByNormalized :many
SELECT id, name, name_normalized
FROM tags
WHERE tayfa_id = @tayfa_id AND name_normalized = ANY(@names::text[]);

-- name: CreateTag :exec
INSERT INTO tags (id, tayfa_id, name, name_normalized)
VALUES (@id, @tayfa_id, @name, @name_normalized);

-- name: GetTag :one
SELECT id, tayfa_id, name
FROM tags
WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: ListTags :many
SELECT t.id, t.tayfa_id, t.name,
       (SELECT count(*) FROM question_tags qt JOIN questions q ON q.id = qt.question_id
        WHERE qt.tag_id = t.id AND q.deleted_at IS NULL)::integer AS question_count
FROM tags t
WHERE t.tayfa_id = @tayfa_id
ORDER BY t.name_normalized, t.id;

-- Etiket silinince bağları CASCADE ile gider; sorular kalır.
-- name: DeleteTag :execrows
DELETE FROM tags WHERE tayfa_id = @tayfa_id AND id = @id;

-- name: ListQuestionTags :many
SELECT qt.question_id, t.id, t.name
FROM question_tags qt
JOIN tags t ON t.id = qt.tag_id
WHERE qt.tayfa_id = @tayfa_id AND qt.question_id = ANY(@question_ids::uuid[])
ORDER BY qt.question_id, t.name_normalized, t.id;

-- name: AddQuestionTag :exec
INSERT INTO question_tags (question_id, tag_id, tayfa_id)
VALUES (@question_id, @tag_id, @tayfa_id)
ON CONFLICT DO NOTHING;

-- name: ClearQuestionTags :exec
DELETE FROM question_tags WHERE tayfa_id = @tayfa_id AND question_id = @question_id;

-- Cevaplar.

-- name: CreateAnswer :exec
INSERT INTO answers (id, question_id, tayfa_id, author_id, body, client_request_id)
VALUES (@id, @question_id, @tayfa_id, @author_id, @body, @client_request_id);

-- name: GetAnswerByClientRequest :one
SELECT id, tayfa_id, question_id, deleted_at
FROM answers
WHERE author_id = @author_id AND client_request_id = @client_request_id;

-- Görünen sorunun görünen cevabı; voted, çağıranın oyu.
-- name: GetAnswer :one
SELECT a.id, a.question_id, a.tayfa_id, a.author_id, a.body, a.vote_count, a.created_at, a.updated_at,
       EXISTS (SELECT 1 FROM answer_votes v WHERE v.answer_id = a.id AND v.user_id = @user_id) AS voted
FROM answers a
JOIN questions q ON q.id = a.question_id AND q.tayfa_id = a.tayfa_id
WHERE a.tayfa_id = @tayfa_id AND a.id = @id AND a.deleted_at IS NULL AND q.deleted_at IS NULL;

-- name: GetAnswerForUpdate :one
SELECT a.id, a.question_id, a.author_id, a.body, a.deleted_at, a.deleted_by,
       q.deleted_at AS question_deleted_at, q.author_id AS question_author_id
FROM answers a
JOIN questions q ON q.id = a.question_id AND q.tayfa_id = a.tayfa_id
WHERE a.tayfa_id = @tayfa_id AND a.id = @id
FOR UPDATE OF a;

-- Faydalı oyu çoktan aza, eşitlikte eskiden yeniye (id artan, UUIDv7).
-- name: ListAnswersPage :many
SELECT a.id, a.question_id, a.tayfa_id, a.author_id, a.body, a.vote_count, a.created_at, a.updated_at,
       EXISTS (SELECT 1 FROM answer_votes v WHERE v.answer_id = a.id AND v.user_id = @user_id) AS voted
FROM answers a
WHERE a.tayfa_id = @tayfa_id AND a.question_id = @question_id AND a.deleted_at IS NULL
  AND (NOT @has_cursor::boolean
       OR a.vote_count < @after_votes::integer
       OR (a.vote_count = @after_votes::integer AND a.id > @after_id::uuid))
ORDER BY a.vote_count DESC, a.id
LIMIT @page_size;

-- name: UpdateAnswerBody :execrows
UPDATE answers
SET body = @body
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: HideAnswer :execrows
UPDATE answers
SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Yazarın kendi silmesi: metin boşaltılır, oyları silinir (ayrı sorgu).
-- name: EraseAnswer :execrows
UPDATE answers
SET deleted_at = @deleted_at, deleted_by = @deleted_by, body = '', vote_count = 0
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- name: DeleteAnswerVotes :exec
DELETE FROM answer_votes WHERE answer_id = @answer_id;

-- name: RestoreAnswer :execrows
UPDATE answers
SET deleted_at = NULL, deleted_by = NULL
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NOT NULL AND char_length(body) > 0;

-- Oy ve sayaç aynı transaction'da: oy eklendiyse sayaç artar. Aynı kişinin
-- ikinci oyu PK'ye takılır, sayaç değişmez.
-- name: AddAnswerVote :execrows
INSERT INTO answer_votes (answer_id, user_id)
VALUES (@answer_id, @user_id)
ON CONFLICT DO NOTHING;

-- name: RemoveAnswerVote :execrows
DELETE FROM answer_votes WHERE answer_id = @answer_id AND user_id = @user_id;

-- name: AdjustAnswerVoteCount :execrows
UPDATE answers
SET vote_count = vote_count + @delta::integer
WHERE tayfa_id = @tayfa_id AND id = @id;
