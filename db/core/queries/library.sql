-- Ortak kütüphane (issue #270; ADR-0040 §1). Tayfa her zaman önce üyelik
-- kontrolüyle yüklenir (internal/core/app/library); buradaki sorgular o
-- tayfanın kimliğiyle çalışır (IDOR). Yetki, modül ve durum makinesi app
-- katmanındadır; ayrılma, hatırlatma ve kapanma internal/core/app/library/
-- lending'dedir.

-- Geri çekilmemiş kalemler, en yeni önce. Kitabın adı sözlükten
-- (birleştirildiyse hedefinin adı) gelir.
-- name: ListLibraryItems :many
SELECT i.id, i.owner_id, i.kind, i.vocabulary_id, i.note, i.photo_upload_id, i.status, i.created_at, i.updated_at,
       COALESCE(tv.value, v.value, i.name)::text AS shown_name,
       (SELECT count(*) FROM library_queue q WHERE q.item_id = i.id)::integer AS queue_length
FROM library_items i
LEFT JOIN tayfa_vocabulary v ON v.id = i.vocabulary_id
LEFT JOIN tayfa_vocabulary tv ON tv.id = v.merged_into
WHERE i.tayfa_id = @tayfa_id AND i.status <> 'withdrawn'
  AND (sqlc.narg(item_id)::uuid IS NULL OR i.id = sqlc.narg(item_id)::uuid)
  AND (sqlc.narg(kind)::text IS NULL OR i.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(owner_id)::uuid IS NULL OR i.owner_id = sqlc.narg(owner_id)::uuid)
ORDER BY i.created_at DESC, i.id;

-- name: GetLibraryItemForUpdate :one
SELECT * FROM library_items
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- name: GetLibraryItemByClientRequest :one
SELECT id FROM library_items
WHERE tayfa_id = @tayfa_id AND client_request_id = @client_request_id;

-- name: CountOwnerLibraryItems :one
SELECT count(*) FROM library_items
WHERE tayfa_id = @tayfa_id AND owner_id = @owner_id AND status <> 'withdrawn';

-- name: CreateLibraryItem :exec
INSERT INTO library_items (
  id, tayfa_id, owner_id, kind, name, vocabulary_id, vocabulary_namespace, note, photo_upload_id,
  client_request_id, created_at, updated_at)
VALUES (
  @id, @tayfa_id, @owner_id, @kind, @name, sqlc.narg(vocabulary_id), sqlc.narg(vocabulary_namespace),
  sqlc.narg(note), sqlc.narg(photo_upload_id), @client_request_id, @now, @now);

-- name: UpdateLibraryItem :execrows
UPDATE library_items
SET name = @name, vocabulary_id = sqlc.narg(vocabulary_id), vocabulary_namespace = sqlc.narg(vocabulary_namespace),
    note = sqlc.narg(note), photo_upload_id = sqlc.narg(photo_upload_id), updated_at = @now
WHERE tayfa_id = @tayfa_id AND id = @id AND status <> 'withdrawn';

-- Teslimde on_loan, iade ve kapanmada available; geri çekilmiş kalem
-- değişmez.
-- name: SetLibraryItemStatus :exec
UPDATE library_items SET status = @status, updated_at = @now
WHERE id = @id AND status <> 'withdrawn';

-- Etkin ödünçler (requested, approved, handed_over).
-- name: ListActiveLibraryLoans :many
SELECT l.id, l.item_id, l.borrower_id, l.state FROM library_loans l
WHERE l.tayfa_id = @tayfa_id AND l.state IN ('requested', 'approved', 'handed_over')
  AND (sqlc.narg(item_id)::uuid IS NULL OR l.item_id = sqlc.narg(item_id)::uuid);

-- name: GetActiveLibraryLoanForUpdate :one
SELECT * FROM library_loans
WHERE item_id = @item_id AND state IN ('requested', 'approved', 'handed_over')
FOR UPDATE;

-- name: GetLibraryLoanForUpdate :one
SELECT * FROM library_loans
WHERE tayfa_id = @tayfa_id AND id = @id
FOR UPDATE;

-- Kişinin ödünçleri (borç alan ya da kalemin sahibi olarak): etkinler
-- önce, sonra en yeniler. loan_id ile tek kayıt.
-- name: ListUserLibraryLoans :many
SELECT l.id, l.item_id, l.borrower_id, l.state, l.requested_at, l.approved_at, l.handed_over_at, l.due_at,
       l.orphaned_at, l.ended_at, i.owner_id, i.kind AS item_kind,
       COALESCE(tv.value, v.value, i.name)::text AS item_name
FROM library_loans l
JOIN library_items i ON i.id = l.item_id
LEFT JOIN tayfa_vocabulary v ON v.id = i.vocabulary_id
LEFT JOIN tayfa_vocabulary tv ON tv.id = v.merged_into
WHERE l.tayfa_id = @tayfa_id AND (l.borrower_id = @user_id OR i.owner_id = @user_id)
  AND (sqlc.narg(loan_id)::uuid IS NULL OR l.id = sqlc.narg(loan_id)::uuid)
ORDER BY (l.ended_at IS NULL) DESC, l.requested_at DESC, l.id
LIMIT 200;

-- name: CreateLibraryLoan :exec
INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at)
VALUES (@id, @item_id, @tayfa_id, @borrower_id, 'requested', @now);

-- name: ApproveLibraryLoan :execrows
UPDATE library_loans SET state = 'approved', approved_at = @now
WHERE id = @id AND state = 'requested';

-- name: HandOverLibraryLoan :execrows
UPDATE library_loans SET state = 'handed_over', handed_over_at = @now, due_at = sqlc.narg(due_at)
WHERE id = @id AND state = 'approved';

-- Ödüncü bitirir: returned, declined, cancelled ya da closed.
-- name: EndLibraryLoan :execrows
UPDATE library_loans SET state = @state, ended_at = @now
WHERE id = @id AND state = ANY(@from_states::text[]);

-- Kişinin sıradaki yeri ve numarası (1 sıradaki).
-- name: GetLibraryQueueRank :one
SELECT (SELECT count(*) FROM library_queue x WHERE x.item_id = q.item_id AND x.position <= q.position)::integer AS rank
FROM library_queue q
WHERE q.item_id = @item_id AND q.user_id = @user_id;

-- name: ListLibraryQueue :many
SELECT q.item_id, q.user_id,
       (SELECT count(*) FROM library_queue x WHERE x.item_id = q.item_id AND x.position <= q.position)::integer AS rank
FROM library_queue q
WHERE q.tayfa_id = @tayfa_id
  AND (sqlc.narg(item_id)::uuid IS NULL OR q.item_id = sqlc.narg(item_id)::uuid)
ORDER BY q.item_id, q.position;

-- name: CountLibraryQueue :one
SELECT count(*) FROM library_queue WHERE item_id = @item_id;

-- Sıranın sonuna ekler; kalem satırı kilitliyken çağrılır.
-- name: AddLibraryQueue :exec
INSERT INTO library_queue (id, item_id, tayfa_id, user_id, position, created_at)
SELECT @id, @item_id, @tayfa_id, @user_id, COALESCE(max(x.position), 0) + 1, @now
FROM library_queue x WHERE x.item_id = @item_id;

-- name: DeleteLibraryQueuePlace :execrows
DELETE FROM library_queue WHERE item_id = @item_id AND user_id = @user_id;

-- Geri çekme (sahip, moderatör, platform): not ve fotoğraf hemen silinir.
-- name: WithdrawLibraryItem :execrows
UPDATE library_items
SET status = 'withdrawn', withdrawn_at = @now, withdrawn_by = sqlc.narg(withdrawn_by),
    note = NULL, photo_upload_id = NULL, updated_at = @now
WHERE tayfa_id = @tayfa_id AND id = @id AND status <> 'withdrawn';

-- name: DeleteLibraryItemsQueue :exec
DELETE FROM library_queue WHERE item_id = ANY(@item_ids::uuid[]);

-- Teslim edilmemiş ödünçler iptal olur.
-- name: CancelPendingLibraryLoans :exec
UPDATE library_loans SET state = 'cancelled', ended_at = @now
WHERE item_id = ANY(@item_ids::uuid[]) AND state IN ('requested', 'approved');

-- Sıradaki kişiye ödünç isteği açar (kalemin etkin ödüncü yokken):
-- sıranın başı silinir, geri çekilmemiş kalem için `requested` ödünç
-- yazılır. Kimse yoksa satır dönmez.
-- name: PromoteLibraryQueue :one
WITH head AS (
  DELETE FROM library_queue q
  WHERE q.id = (SELECT x.id FROM library_queue x WHERE x.item_id = @item_id ORDER BY x.position LIMIT 1)
  RETURNING q.item_id, q.tayfa_id, q.user_id
)
INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at)
SELECT @id, h.item_id, h.tayfa_id, h.user_id, 'requested', @now
FROM head h
JOIN library_items i ON i.id = h.item_id AND i.status <> 'withdrawn'
RETURNING borrower_id;

-- Sıra sende bildirimi (lending.Promote, issue #332): kalemin tayfası ve
-- sahibi (engel kontrolü için).
-- name: GetLibraryItemParties :one
SELECT tayfa_id, owner_id FROM library_items WHERE id = @id;

-- Ayrılma (lending.Departed): kişinin kalemleri geri çekilir. tayfa_id
-- NULL: bütün tayfalar (hesap silme).
-- name: WithdrawUserLibraryItems :many
UPDATE library_items
SET status = 'withdrawn', withdrawn_at = @now, note = NULL, photo_upload_id = NULL, updated_at = @now
WHERE owner_id = @user_id AND status <> 'withdrawn'
  AND (sqlc.narg(tayfa_id)::uuid IS NULL OR tayfa_id = sqlc.narg(tayfa_id)::uuid)
RETURNING id;

-- name: DeleteUserLibraryQueuePlaces :exec
DELETE FROM library_queue
WHERE user_id = @user_id AND (sqlc.narg(tayfa_id)::uuid IS NULL OR tayfa_id = sqlc.narg(tayfa_id)::uuid);

-- Kişinin teslim almadığı istekleri iptal olur; sıradakiler için kalemler
-- döner.
-- name: CancelUserLibraryRequests :many
UPDATE library_loans SET state = 'cancelled', ended_at = @now
WHERE borrower_id = @user_id AND state IN ('requested', 'approved')
  AND (sqlc.narg(tayfa_id)::uuid IS NULL OR tayfa_id = sqlc.narg(tayfa_id)::uuid)
RETURNING item_id;

-- Teslim edilmiş ödünçte taraflardan biri ayrıldı: kayıt kapanmamış kalır.
-- name: OrphanUserLibraryLoans :many
UPDATE library_loans AS l SET orphaned_at = @now
FROM library_items AS i
WHERE i.id = l.item_id AND l.state = 'handed_over' AND l.orphaned_at IS NULL
  AND (l.borrower_id = @user_id OR i.owner_id = @user_id)
  AND (sqlc.narg(tayfa_id)::uuid IS NULL OR l.tayfa_id = sqlc.narg(tayfa_id)::uuid)
RETURNING l.id, l.tayfa_id, l.borrower_id, i.owner_id;
