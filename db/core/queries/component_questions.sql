-- Soru toplama bileşeni (`questions`; issue #256; ADR-0025 §4, §5). Etkinlik
-- her zaman önce tayfa kapsamında yüklenir ve bileşen etkinliğin tür
-- sürümünde çözülür (internal/core/app/component: View / Change); buradaki
-- sorgular o etkinliğin kimliği ve bileşenin anahtarıyla çalışır (IDOR).
-- Yetki ve anonim sorunun yazarını gizleme app katmanındadır
-- (internal/core/app/component/questions): author_id istemciye anonim
-- soruda hiçbir zaman dönmez.

-- name: GetEventQuestionByClientRequest :one
SELECT id, event_id, component_key, deleted_at FROM event_questions
WHERE author_id = @author_id AND client_request_id = @client_request_id;

-- name: CountUserEventQuestions :one
SELECT count(*) FROM event_questions
WHERE event_id = @event_id AND component_key = @component_key AND author_id = @author_id AND deleted_at IS NULL;

-- name: CountEventQuestions :one
SELECT count(*) FROM event_questions
WHERE event_id = @event_id AND component_key = @component_key AND deleted_at IS NULL;

-- name: CreateEventQuestion :exec
INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, client_request_id, created_at)
VALUES (@id, @event_id, @component_key, @author_id, @anonymous, @body, @client_request_id, @created_at);

-- Oy sayısı okunurken sayılır; viewer_id'nin oyu ayrıca döner.
-- name: GetEventQuestion :one
SELECT q.id, q.author_id, q.anonymous, q.body, q.answered_at, q.created_at,
  (SELECT count(*) FROM event_question_votes v WHERE v.question_id = q.id)::int AS vote_count,
  (EXISTS (SELECT 1 FROM event_question_votes v WHERE v.question_id = q.id AND v.user_id = @viewer_id))::boolean AS caller_voted
FROM event_questions q
WHERE q.event_id = @event_id AND q.component_key = @component_key AND q.id = @id AND q.deleted_at IS NULL;

-- Cevaplanmamışlar önce, sonra çok oy alan, sonra eski. Bileşen başına
-- canlı soru sayısı uygulamada sınırlıdır (questions.MaxQuestions).
-- name: ListEventQuestions :many
SELECT q.id, q.author_id, q.anonymous, q.body, q.answered_at, q.created_at,
  (SELECT count(*) FROM event_question_votes v WHERE v.question_id = q.id)::int AS vote_count,
  (EXISTS (SELECT 1 FROM event_question_votes v WHERE v.question_id = q.id AND v.user_id = @viewer_id))::boolean AS caller_voted
FROM event_questions q
WHERE q.event_id = @event_id AND q.component_key = @component_key AND q.deleted_at IS NULL
ORDER BY (q.answered_at IS NOT NULL), vote_count DESC, q.created_at, q.id
LIMIT 500;

-- name: DeleteEventQuestion :execrows
UPDATE event_questions SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE event_id = @event_id AND component_key = @component_key AND id = @id AND deleted_at IS NULL;

-- İlk işaret kalır; yeniden işaretleme değiştirmez.
-- name: MarkEventQuestionAnswered :execrows
UPDATE event_questions SET answered_at = @answered_at, answered_by = @answered_by
WHERE event_id = @event_id AND component_key = @component_key AND id = @id AND deleted_at IS NULL
  AND answered_at IS NULL;

-- name: UnmarkEventQuestionAnswered :execrows
UPDATE event_questions SET answered_at = NULL, answered_by = NULL
WHERE event_id = @event_id AND component_key = @component_key AND id = @id AND deleted_at IS NULL
  AND answered_at IS NOT NULL;

-- name: CreateEventQuestionVote :execrows
INSERT INTO event_question_votes (question_id, event_id, component_key, user_id, created_at)
VALUES (@question_id, @event_id, @component_key, @user_id, @created_at)
ON CONFLICT (question_id, user_id) DO NOTHING;

-- name: DeleteEventQuestionVote :execrows
DELETE FROM event_question_votes
WHERE question_id = @question_id AND event_id = @event_id AND component_key = @component_key AND user_id = @user_id;
