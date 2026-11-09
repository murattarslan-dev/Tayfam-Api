-- Birebir mesajlar (issue #95; 00006_direct_messages.sql). Taraflar her
-- zaman user_low_id < user_high_id sırasıyla verilir. Bir kullanıcının
-- erişebildiği konuşma yalnızca taraf olduğu konuşmadır: her okuma
-- sorgusu konuşma kimliğinin yanında kullanıcıyı da alır (IDOR).

-- Konuşmalar.

-- name: GetConversationByPair :one
SELECT id, user_low_id, user_high_id, status, requested_by, accepted_at, last_message_at, created_at
FROM dm_conversations
WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id;

-- Aynı iki kişinin eşzamanlı gönderimleri sırayla işlenir.
-- name: LockConversationByPair :one
SELECT id, user_low_id, user_high_id, status, requested_by, accepted_at, last_message_at, created_at
FROM dm_conversations
WHERE user_low_id = @user_low_id AND user_high_id = @user_high_id
FOR UPDATE;

-- Kişinin taraf olduğu konuşma; başkasınınki "yok".
-- name: GetConversationForUser :one
SELECT id, user_low_id, user_high_id, status, requested_by, accepted_at, last_message_at, created_at
FROM dm_conversations
WHERE id = @id AND (user_low_id = @user_id OR user_high_id = @user_id);

-- Satır varsa (eşzamanlı ilk mesaj) hiçbir şey yapmaz; sonuç boş döner.
-- name: CreateConversation :one
INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, requested_by, accepted_at, last_message_at)
VALUES (@id, @user_low_id, @user_high_id, @status, sqlc.narg(requested_by), sqlc.narg(accepted_at), @last_message_at)
ON CONFLICT (user_low_id, user_high_id) DO NOTHING
RETURNING id, user_low_id, user_high_id, status, requested_by, accepted_at, last_message_at, created_at;

-- İstek kabul edildi (alıcı kabul etti ya da cevap yazdı; reddettiği
-- isteğe yazması da onu reddedilenlerden çıkarır).
-- name: AcceptConversation :execrows
UPDATE dm_conversations SET status = 'active', accepted_at = @accepted_at, declined_at = NULL
WHERE id = @id AND status <> 'active';

-- Alıcı isteği reddetti.
-- name: DeclineConversation :execrows
UPDATE dm_conversations SET status = 'declined', declined_at = @declined_at
WHERE id = @id AND status = 'request';

-- Kişinin reddettiği istekler ("reddedilenler", issue #101), en son
-- reddedilen önce; imleç (declined_at, id). Gönderenin hesabı silinmiş
-- istek listelenmez (kimse yazamaz). Gönderen bu listeyi hiç görmez:
-- requested_by çağıran olamaz.
-- name: ListDeclinedRequests :many
SELECT id, requested_by, declined_at, created_at
FROM dm_conversations
WHERE status = 'declined'
  AND (user_low_id = @user_id::uuid OR user_high_id = @user_id::uuid)
  AND requested_by IS NOT NULL AND requested_by <> @user_id::uuid
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (declined_at, id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY declined_at DESC, id DESC
LIMIT @n;

-- Reddedeni listesinden çıkarır: konuşma bekleyen isteğe döner. Yalnızca
-- reddeden taraf (istek açan değil) çıkarabilir; başkasınınki etkilenmez.
-- name: RestoreDeclinedRequest :execrows
UPDATE dm_conversations SET status = 'request', declined_at = NULL
WHERE id = @id AND status = 'declined'
  AND (user_low_id = @user_id::uuid OR user_high_id = @user_id::uuid)
  AND requested_by IS NOT NULL AND requested_by <> @user_id::uuid;

-- name: TouchConversation :exec
UPDATE dm_conversations SET last_message_at = GREATEST(last_message_at, @at)
WHERE id = @id;

-- Son 24 saatte istek olarak açılan konuşmalar (v7 günlük sınırı). Durumun
-- sonradan değişmesi (kabul, ret) sayımı değiştirmez.
-- name: CountRecentRequests :one
SELECT count(*) FROM dm_conversations
WHERE requested_by = @requested_by AND created_at > @since;

-- Kişinin günlük istek sayımını sıraya sokar: aynı kişinin eşzamanlı
-- istekleri sınırı birlikte aşamaz.
-- name: LockRequestLimit :exec
SELECT pg_advisory_xact_lock(hashtextextended('tayfam.chat.dm_requests:' || sqlc.arg(user_id)::uuid::text, 0));

-- Gönderenin konuşmadaki mesaj sayısı (istek durumunda tek mesaj; silinen
-- de sayılır).
-- name: CountSenderMessages :one
SELECT count(*) FROM dm_messages
WHERE conversation_id = @conversation_id AND sender_id = @sender_id;

-- Kişinin konuşmaları, son mesaja göre yeniden eskiye. Reddettiği istekler
-- listelenmez; gönderen reddedilen isteğini görmeye devam eder (v7: bunu
-- bilmez). unread karşı tarafın okunmamış, silinmemiş mesajlarıdır (en
-- fazla 100). peer_last_read_message_id karşı tarafın okundu işaretidir
-- (okundu bilgisi; uygulama yalnızca active konuşmada gösterir). İmleç
-- (last_message_at, id).
-- name: ListConversationsPage :many
SELECT c.id, c.user_low_id, c.user_high_id, c.status, c.requested_by, c.accepted_at, c.last_message_at, c.created_at,
       r.last_read_message_id, p.last_read_message_id AS peer_last_read_message_id,
       (SELECT count(*) FROM (
          SELECT 1 FROM dm_messages m
          WHERE m.conversation_id = c.id AND m.deleted_at IS NULL
            AND m.sender_id IS DISTINCT FROM @user_id::uuid
            AND (r.last_read_message_id IS NULL OR m.id > r.last_read_message_id)
          LIMIT 100) u)::int AS unread
FROM dm_conversations c
LEFT JOIN dm_conversation_reads r ON r.conversation_id = c.id AND r.user_id = @user_id::uuid
LEFT JOIN dm_conversation_reads p ON p.conversation_id = c.id AND p.user_id <> @user_id::uuid
WHERE (c.user_low_id = @user_id::uuid OR c.user_high_id = @user_id::uuid)
  AND NOT (c.status = 'declined' AND c.requested_by IS DISTINCT FROM @user_id::uuid)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (c.last_message_at, c.id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY c.last_message_at DESC, c.id DESC
LIMIT @n;

-- Tek konuşma, listedeki sütunlarla (kişi taraf değilse boş).
-- name: GetConversationView :one
SELECT c.id, c.user_low_id, c.user_high_id, c.status, c.requested_by, c.accepted_at, c.last_message_at, c.created_at,
       r.last_read_message_id, p.last_read_message_id AS peer_last_read_message_id,
       (SELECT count(*) FROM (
          SELECT 1 FROM dm_messages m
          WHERE m.conversation_id = c.id AND m.deleted_at IS NULL
            AND m.sender_id IS DISTINCT FROM @user_id::uuid
            AND (r.last_read_message_id IS NULL OR m.id > r.last_read_message_id)
          LIMIT 100) u)::int AS unread
FROM dm_conversations c
LEFT JOIN dm_conversation_reads r ON r.conversation_id = c.id AND r.user_id = @user_id::uuid
LEFT JOIN dm_conversation_reads p ON p.conversation_id = c.id AND p.user_id <> @user_id::uuid
WHERE c.id = @id AND (c.user_low_id = @user_id::uuid OR c.user_high_id = @user_id::uuid);

-- Mesajlar.

-- name: GetDirectMessageByRequest :one
SELECT id, conversation_id, sender_id, body_encrypted, key_version, created_at, deleted_at
FROM dm_messages
WHERE sender_id = @sender_id AND client_request_id = @client_request_id;

-- Aynı (gönderen, client_request_id) varsa satır açılmaz ve sonuç boş döner.
-- name: CreateDirectMessage :one
INSERT INTO dm_messages (id, conversation_id, sender_id, body_encrypted, key_version, client_request_id)
VALUES (@id, @conversation_id, @sender_id, @body_encrypted, @key_version, @client_request_id)
ON CONFLICT (sender_id, client_request_id) DO NOTHING
RETURNING id, conversation_id, sender_id, body_encrypted, key_version, created_at, deleted_at;

-- En yeni önce; before_id verilirse ondan eskiler.
-- name: ListDirectMessagesBefore :many
SELECT id, conversation_id, sender_id, body_encrypted, key_version, created_at, deleted_at
FROM dm_messages
WHERE conversation_id = @conversation_id AND (sqlc.narg(before_id)::uuid IS NULL OR id < sqlc.narg(before_id)::uuid)
ORDER BY id DESC
LIMIT @n;

-- En eski önce, after_id'den yeniler: kaçırılan mesajları tamamlama
-- (ADR-0005).
-- name: ListDirectMessagesAfter :many
SELECT id, conversation_id, sender_id, body_encrypted, key_version, created_at, deleted_at
FROM dm_messages
WHERE conversation_id = @conversation_id AND id > @after_id
ORDER BY id
LIMIT @n;

-- name: GetDirectMessage :one
SELECT id, conversation_id, sender_id, body_encrypted, key_version, created_at, deleted_at
FROM dm_messages
WHERE conversation_id = @conversation_id AND id = @id;

-- Gönderenin kendi silmesi: metin hemen gider, geri alınamaz.
-- name: EraseDirectMessage :execrows
UPDATE dm_messages
SET body_encrypted = NULL, key_version = NULL, deleted_at = @deleted_at
WHERE conversation_id = @conversation_id AND id = @id AND sender_id = @sender_id AND deleted_at IS NULL;

-- Şikâyet doğrulaması (core'un sorusu, issue #95): kişinin taraf olduğu
-- konuşmadaki mesajın konuşması ve göndereni. İçerik seçilmez. Silinmiş
-- mesaj da bulunur (taciz edip silen şikâyetten kaçamasın); reddedilmiş
-- istek de (isteği reddeden şikâyet edebilsin).
-- name: GetDirectMessageForParty :one
SELECT m.conversation_id, m.sender_id
FROM dm_messages m
JOIN dm_conversations c ON c.id = m.conversation_id
WHERE m.id = @id AND (c.user_low_id = @user_id OR c.user_high_id = @user_id);

-- Okunma bilgisi.

-- Okundu işareti yalnızca ileri gider (UUIDv7 zaman sıralı).
-- name: MarkConversationRead :exec
INSERT INTO dm_conversation_reads (conversation_id, user_id, last_read_message_id, read_at)
VALUES (@conversation_id, @user_id, @message_id, @read_at)
ON CONFLICT (conversation_id, user_id) DO UPDATE
SET last_read_message_id = GREATEST(dm_conversation_reads.last_read_message_id, EXCLUDED.last_read_message_id),
    read_at = CASE WHEN EXCLUDED.last_read_message_id > dm_conversation_reads.last_read_message_id
                   THEN EXCLUDED.read_at ELSE dm_conversation_reads.read_at END;

-- Karşı tarafın okunma işareti (okundu bilgisi, v14 "Okunma bilgisi").
-- name: GetConversationRead :one
SELECT last_read_message_id, read_at FROM dm_conversation_reads
WHERE conversation_id = @conversation_id AND user_id = @user_id;

-- Bildirimler: konuşma başına okunmamış tek "yeni mesaj" bildirimi.

-- Kişinin konuşmadaki okunmamış mesajları: karşı tarafın, silinmemiş,
-- okundu işaretinden sonraki mesajları (en fazla 100; liste ile aynı).
-- name: CountUnreadDirectMessages :one
SELECT count(*)::int FROM (
  SELECT 1 FROM dm_messages m
  LEFT JOIN dm_conversation_reads r ON r.conversation_id = m.conversation_id AND r.user_id = @user_id::uuid
  WHERE m.conversation_id = @conversation_id AND m.deleted_at IS NULL
    AND m.sender_id IS DISTINCT FROM @user_id::uuid
    AND (r.last_read_message_id IS NULL OR m.id > r.last_read_message_id)
  LIMIT 100
) u;

-- Bekleyen (okunmamış) konuşma bildiriminin sayacını günceller ve türünü
-- döner; yoksa sonuç boştur (yeni bildirim açılır).
-- name: UpdateConversationNotificationUnread :one
UPDATE notifications SET unread_count = @unread_count
WHERE user_id = @user_id AND target_type = 'dm_conversation' AND target_id = @conversation_id AND read_at IS NULL
RETURNING type;

-- Konuşma okununca o konuşmanın bildirimleri de okunmuş olur.
-- name: MarkConversationNotificationsRead :exec
UPDATE notifications SET read_at = @read_at
WHERE user_id = @user_id AND target_type = 'dm_conversation' AND target_id = @conversation_id AND read_at IS NULL;

-- Saklama ve silme.

-- 30 gün: silinmiş mesajlardan bir parti kalıcı silinir.
-- name: PurgeDeletedDirectMessages :execrows
DELETE FROM dm_messages
WHERE id IN (
  SELECT o.id FROM dm_messages o
  WHERE o.deleted_at < sqlc.arg(before)::timestamptz
  ORDER BY o.deleted_at
  LIMIT sqlc.arg(n)
);

-- Hesap silindi (user.deleted; v14 "İlişkiler ve Silme Davranışı"): kişinin
-- tarafı, isteği ve gönderen alanı NULL olur, okunma kayıtları silinir; iki
-- tarafı da silinmiş konuşma mesajlarıyla birlikte gider.
-- name: ClearConversationLowSide :exec
UPDATE dm_conversations
SET user_low_id = NULL, requested_by = CASE WHEN requested_by = @user_id THEN NULL ELSE requested_by END
WHERE user_low_id = @user_id;

-- name: ClearConversationHighSide :exec
UPDATE dm_conversations
SET user_high_id = NULL, requested_by = CASE WHEN requested_by = @user_id THEN NULL ELSE requested_by END
WHERE user_high_id = @user_id;

-- name: ClearDirectMessageSender :exec
UPDATE dm_messages SET sender_id = NULL WHERE sender_id = @user_id;

-- name: DeleteUserConversationReads :exec
DELETE FROM dm_conversation_reads WHERE user_id = @user_id;

-- name: DeleteAbandonedConversations :exec
DELETE FROM dm_conversations WHERE user_low_id IS NULL AND user_high_id IS NULL;
