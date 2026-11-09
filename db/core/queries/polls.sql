-- Anketler (issue #60). Her sorgu tayfa_id ile sınırlıdır (IDOR); üyelik ve
-- izin kontrolü app katmanındadır (access, authz).
--
-- Gizli anket: kimin hangi seçeneğe oy verdiğini döndüren tek sorgu
-- ListPollVoters'tır ve yalnızca gizli olmayan ankette sonuç verir
-- (anonymous = false koşulu sorgunun içindedir). Kişinin kendi oyu
-- ListOwnPollVotes ile, yalnızca o kişi için okunur. Sayımlar seçenek
-- başına toplamdır.

-- name: CreatePoll :exec
INSERT INTO polls (id, tayfa_id, event_id, created_by, question, anonymous, multiple_choice, closes_at, created_at)
VALUES (@id, @tayfa_id, sqlc.narg('event_id'), @created_by, @question, @anonymous, @multiple_choice, sqlc.narg('closes_at'), @created_at);

-- name: CreatePollOption :exec
INSERT INTO poll_options (id, poll_id, text, position)
VALUES (@id, @poll_id, @text, @position);

-- Etkinliğe bağlanacak anket için: etkinlik bu tayfada ve silinmemiş.
-- name: PollEventExists :one
SELECT EXISTS (
  SELECT 1 FROM events WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL
);

-- name: GetPoll :one
SELECT id, tayfa_id, event_id, created_by, question, anonymous, multiple_choice, closes_at, closed_at, created_at
FROM polls
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Yazma işlemleri anket satırını kilitleyerek okur.
-- name: GetPollForUpdate :one
SELECT id, tayfa_id, event_id, created_by, question, anonymous, multiple_choice, closes_at, closed_at, created_at
FROM polls
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- Yeniden eskiye; isteğe bağlı olarak tek bir etkinliğin anketleri. Sayfa,
-- önceki sayfanın son satırından sonra başlar.
-- name: ListPollsPage :many
SELECT id, tayfa_id, event_id, created_by, question, anonymous, multiple_choice, closes_at, closed_at, created_at
FROM polls
WHERE tayfa_id = @tayfa_id
  AND deleted_at IS NULL
  AND (sqlc.narg('event_id')::uuid IS NULL OR event_id = sqlc.narg('event_id')::uuid)
  AND (NOT @has_cursor::boolean
       OR (created_at, id) < (@after_created_at::timestamptz, @after_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- Seçenekler ve seçenek başına oy sayısı; kimin oy verdiği dönmez.
-- name: ListPollOptionsWithCounts :many
SELECT o.poll_id, o.id, o.text, o.position,
       (SELECT count(*) FROM poll_votes v WHERE v.poll_id = o.poll_id AND v.option_id = o.id)::bigint AS vote_count
FROM poll_options o
WHERE o.poll_id = ANY(@poll_ids::uuid[])
ORDER BY o.poll_id, o.position;

-- Oy veren kişi sayısı (çoklu seçimde bir kişi bir kez sayılır).
-- name: CountPollVoters :many
SELECT poll_id, count(DISTINCT user_id)::bigint AS voters
FROM poll_votes
WHERE poll_id = ANY(@poll_ids::uuid[])
GROUP BY poll_id;

-- Yalnızca çağıranın kendi oyları.
-- name: ListOwnPollVotes :many
SELECT poll_id, option_id
FROM poll_votes
WHERE poll_id = ANY(@poll_ids::uuid[]) AND user_id = @user_id;

-- name: ListPollOptionIDs :many
SELECT id FROM poll_options WHERE poll_id = @poll_id;

-- Oy değiştirmek eski oyu siler (v14).
-- name: DeleteOwnPollVotes :execrows
DELETE FROM poll_votes WHERE poll_id = @poll_id AND user_id = @user_id;

-- name: CreatePollVote :exec
INSERT INTO poll_votes (poll_id, option_id, user_id, created_at)
VALUES (@poll_id, @option_id, @user_id, @created_at);

-- Açık ankette bir seçeneğe oy verenler, oy sırasıyla. Gizli ankette hiç
-- satır dönmez: koşul sorgunun kendisindedir, app katmanındaki kontrol
-- ikinci savunma değil birincisidir.
-- name: ListPollVoters :many
SELECT v.user_id, v.created_at
FROM poll_votes v
JOIN polls p ON p.id = v.poll_id
WHERE p.tayfa_id = @tayfa_id AND v.poll_id = @poll_id AND v.option_id = @option_id
  AND p.anonymous = false AND p.deleted_at IS NULL
  AND (NOT @has_cursor::boolean
       OR (v.created_at, v.user_id) > (@after_created_at::timestamptz, @after_user_id::uuid))
ORDER BY v.created_at, v.user_id
LIMIT @page_size;

-- name: ClosePoll :execrows
UPDATE polls
SET closed_at = @closed_at
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL AND closed_at IS NULL;

-- name: DeletePoll :execrows
UPDATE polls
SET deleted_at = @deleted_at, deleted_by = @deleted_by
WHERE tayfa_id = @tayfa_id AND id = @id AND deleted_at IS NULL;

-- Silinen anketin oyları hemen silinir; geri alma yoktur.
-- name: DeletePollVotes :exec
DELETE FROM poll_votes WHERE poll_id = @poll_id;
