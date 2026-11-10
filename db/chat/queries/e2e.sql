-- Birebir mesajda uçtan uca şifreleme, sunucu tarafı (issue #127; ADR-0018;
-- 00010_e2e.sql). Her sorgu cihazı ya kullanıcıyla ya da oturumla birlikte
-- alır: bir kullanıcı yalnızca kendi cihazını, kendi zarflarını, kendi
-- arşivini ve kendi eşleştirme isteklerini görür (IDOR).

-- Kullanıcı durumu.

-- name: EnsureE2EState :exec
INSERT INTO user_e2e_state (user_id) VALUES (@user_id)
ON CONFLICT (user_id) DO NOTHING;

-- Aynı kullanıcının cihaz kaydı, eşleştirmesi ve anahtar yenilemesi sırayla
-- işlenir.
-- name: LockE2EState :one
SELECT user_id, history_key_version, rotation_needed, last_device_number, sessions_ended_at
FROM user_e2e_state
WHERE user_id = @user_id
FOR UPDATE;

-- name: GetE2EState :one
SELECT user_id, history_key_version, rotation_needed, last_device_number, sessions_ended_at
FROM user_e2e_state
WHERE user_id = @user_id;

-- name: SetLastDeviceNumber :exec
UPDATE user_e2e_state SET last_device_number = @last_device_number WHERE user_id = @user_id;

-- Yeni geçmiş anahtarı (ilk cihaz ya da yenileme): sürüm bir artar.
-- name: BumpHistoryKeyVersion :one
UPDATE user_e2e_state SET history_key_version = history_key_version + 1, rotation_needed = false
WHERE user_id = @user_id
RETURNING history_key_version;

-- name: SetRotationNeeded :exec
UPDATE user_e2e_state SET rotation_needed = @rotation_needed WHERE user_id = @user_id;

-- Yaptırım: bu andan önce verilmiş token'lar cihaz kaydedemez.
-- name: EndUserSessions :exec
INSERT INTO user_e2e_state (user_id, sessions_ended_at) VALUES (@user_id, @ended_at)
ON CONFLICT (user_id) DO UPDATE
SET sessions_ended_at = GREATEST(user_e2e_state.sessions_ended_at, EXCLUDED.sessions_ended_at);

-- name: DeleteE2EState :exec
DELETE FROM user_e2e_state WHERE user_id = @user_id;

-- Biten oturumlar.

-- name: RecordEndedSession :exec
INSERT INTO e2e_ended_sessions (session_id, ended_at) VALUES (@session_id, @ended_at)
ON CONFLICT (session_id) DO NOTHING;

-- name: IsSessionEnded :one
SELECT EXISTS (SELECT 1 FROM e2e_ended_sessions WHERE session_id = @session_id);

-- name: PurgeEndedSessions :execrows
DELETE FROM e2e_ended_sessions WHERE ended_at < @before;

-- Cihazlar.

-- name: CreateDevice :one
INSERT INTO device_keys (
  id, user_id, session_id, device_number, registration_id, identity_key,
  signed_prekey_id, signed_prekey, signed_prekey_signature,
  kyber_prekey_id, kyber_prekey, kyber_prekey_signature, created_at, prekeys_updated_at, last_seen_at
) VALUES (
  @id, @user_id, @session_id, @device_number, @registration_id, @identity_key,
  @signed_prekey_id, @signed_prekey, @signed_prekey_signature,
  @kyber_prekey_id, @kyber_prekey, @kyber_prekey_signature, @created_at, @created_at, @created_at
)
RETURNING id, user_id, session_id, device_number, registration_id, identity_key,
  signed_prekey_id, signed_prekey, signed_prekey_signature,
  kyber_prekey_id, kyber_prekey, kyber_prekey_signature, paired_at, created_at, last_seen_at;

-- Oturumun cihazı (çağıranın kendi cihazı).
-- name: GetDeviceBySession :one
SELECT id, user_id, session_id, device_number, registration_id, identity_key,
  signed_prekey_id, signed_prekey, signed_prekey_signature,
  kyber_prekey_id, kyber_prekey, kyber_prekey_signature, paired_at, created_at, last_seen_at
FROM device_keys
WHERE session_id = @session_id AND user_id = @user_id;

-- name: LockDeviceBySession :one
SELECT id, user_id, session_id, device_number, registration_id, identity_key,
  signed_prekey_id, signed_prekey, signed_prekey_signature,
  kyber_prekey_id, kyber_prekey, kyber_prekey_signature, paired_at, created_at, last_seen_at
FROM device_keys
WHERE session_id = @session_id AND user_id = @user_id
FOR UPDATE;

-- Kullanıcının cihazları, numaraya göre.
-- name: ListUserDevices :many
SELECT id, user_id, session_id, device_number, registration_id, identity_key,
  signed_prekey_id, signed_prekey, signed_prekey_signature,
  kyber_prekey_id, kyber_prekey, kyber_prekey_signature, paired_at, created_at, last_seen_at
FROM device_keys
WHERE user_id = @user_id
ORDER BY device_number;

-- Düz gönderimde E2E zorunluluğu: taraflardan birinin cihazı var mı?
-- name: AnyUserHasDevice :one
SELECT EXISTS (SELECT 1 FROM device_keys WHERE user_id = ANY (@user_ids::uuid[]));

-- name: DeleteDevice :one
DELETE FROM device_keys WHERE id = @id AND user_id = @user_id
RETURNING id, user_id, device_number, paired_at;

-- name: DeleteDeviceBySession :one
DELETE FROM device_keys WHERE session_id = @session_id
RETURNING id, user_id, device_number, paired_at;

-- name: DeleteUserDevices :many
DELETE FROM device_keys WHERE user_id = @user_id
RETURNING id, user_id, device_number, paired_at;

-- name: UpdateSignedPreKey :exec
UPDATE device_keys
SET signed_prekey_id = @signed_prekey_id, signed_prekey = @signed_prekey,
    signed_prekey_signature = @signed_prekey_signature, prekeys_updated_at = @at
WHERE id = @id;

-- name: UpdateKyberLastResortPreKey :exec
UPDATE device_keys
SET kyber_prekey_id = @kyber_prekey_id, kyber_prekey = @kyber_prekey,
    kyber_prekey_signature = @kyber_prekey_signature, prekeys_updated_at = @at
WHERE id = @id;

-- name: PairDevice :execrows
UPDATE device_keys SET paired_at = @paired_at WHERE id = @id AND paired_at IS NULL;

-- Görülme saatte en çok bir kez yazılır.
-- name: TouchDevice :exec
UPDATE device_keys SET last_seen_at = @now WHERE id = @id AND last_seen_at < @stale_before;

-- 90 gün görünmeyen cihazlar.
-- name: ListUnseenDevices :many
SELECT id, user_id FROM device_keys
WHERE last_seen_at < @before
ORDER BY last_seen_at
LIMIT @n;

-- Tek kullanımlık ön anahtarlar.

-- Var olan anahtar kimliği yeniden yüklenirse değişmez (tekrar deneme).
-- name: InsertECPreKeys :execrows
INSERT INTO device_one_time_prekeys (device_id, kind, key_id, public_key)
SELECT @device_id::uuid, 'ec', unnest(@key_ids::int[]), unnest(@public_keys::bytea[])
ON CONFLICT (device_id, kind, key_id) DO NOTHING;

-- name: InsertKyberPreKeys :execrows
INSERT INTO device_one_time_prekeys (device_id, kind, key_id, public_key, signature)
SELECT @device_id::uuid, 'kyber', unnest(@key_ids::int[]), unnest(@public_keys::bytea[]), unnest(@signatures::bytea[])
ON CONFLICT (device_id, kind, key_id) DO NOTHING;

-- name: CountOneTimePreKeys :one
SELECT count(*) FILTER (WHERE kind = 'ec')::int AS ec, count(*) FILTER (WHERE kind = 'kyber')::int AS kyber
FROM device_one_time_prekeys
WHERE device_id = @device_id;

-- Bir anahtar bir kez verilir: eşzamanlı çekmeler kilitli satırı atlar,
-- seçilen satır aynı ifadede silinir. Kalmadıysa sonuç boştur.
-- name: ClaimOneTimePreKey :one
DELETE FROM device_one_time_prekeys
WHERE (device_id, kind, key_id) = (
  SELECT p.device_id, p.kind, p.key_id FROM device_one_time_prekeys p
  WHERE p.device_id = @device_id AND p.kind = @kind
  ORDER BY p.key_id
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
RETURNING key_id, public_key, signature;

-- Anahtar olayları ("güvenlik kodu değişti").

-- name: InsertKeyEvent :exec
INSERT INTO user_key_events (id, user_id, device_id, device_number, event_type, created_at)
VALUES (@id, @user_id, @device_id, @device_number, @event_type, @created_at);

-- En yeni önce; imleç olay kimliği.
-- name: ListKeyEvents :many
SELECT id, device_id, device_number, event_type, created_at
FROM user_key_events
WHERE user_id = @user_id AND (sqlc.narg(before_id)::uuid IS NULL OR id < sqlc.narg(before_id)::uuid)
ORDER BY id DESC
LIMIT @n;

-- name: PurgeKeyEvents :execrows
DELETE FROM user_key_events WHERE created_at < @before;

-- name: DeleteUserKeyEvents :exec
DELETE FROM user_key_events WHERE user_id = @user_id;

-- Zarflar.

-- name: CreateDirectMessageE2E :one
INSERT INTO dm_messages (id, conversation_id, sender_id, sender_device_id, encryption, client_request_id)
VALUES (@id, @conversation_id, @sender_id, @sender_device_id, 'e2e', @client_request_id)
ON CONFLICT (sender_id, client_request_id) DO NOTHING
RETURNING id, conversation_id, sender_id, encryption, body_encrypted, key_version, created_at, deleted_at;

-- name: InsertEnvelope :exec
INSERT INTO dm_envelopes (id, message_id, recipient_device_id, sender_id, sender_device_id, sender_device_number, message_type, ciphertext, created_at)
VALUES (@id, @message_id, @recipient_device_id, @sender_id, @sender_device_id, @sender_device_number, @message_type, @ciphertext, @created_at);

-- Cihazın bekleyen zarfları, en eski önce; imleç zarf kimliği.
-- name: ListEnvelopes :many
SELECT e.id, e.message_id, m.conversation_id, e.sender_id, e.sender_device_id, e.sender_device_number,
       e.message_type, e.ciphertext, e.created_at
FROM dm_envelopes e
JOIN dm_messages m ON m.id = e.message_id
WHERE e.recipient_device_id = @device_id AND (sqlc.narg(after_id)::uuid IS NULL OR e.id > sqlc.narg(after_id)::uuid)
ORDER BY e.id
LIMIT @n;

-- Yalnızca çağıranın cihazına ait zarflar silinir.
-- name: AckEnvelopes :execrows
DELETE FROM dm_envelopes WHERE recipient_device_id = @device_id AND id = ANY (@ids::uuid[]);

-- name: DeleteMessageEnvelopes :exec
DELETE FROM dm_envelopes WHERE message_id = @message_id;

-- name: DeleteEnvelopesFromSender :exec
DELETE FROM dm_envelopes WHERE sender_id = @sender_id;

-- name: PurgeEnvelopes :execrows
DELETE FROM dm_envelopes
WHERE id IN (SELECT o.id FROM dm_envelopes o WHERE o.created_at < @before ORDER BY o.created_at LIMIT @n);

-- Geçmiş arşivi.

-- Kullanıcının taraf olduğu, silinmemiş mesajlar yazılır; diğerleri
-- atlanır. Var olan kayıt değişmez (ilk yazan kazanır).
-- name: InsertArchiveEntries :exec
INSERT INTO dm_archive_entries (user_id, message_id, conversation_id, key_version, ciphertext, created_at)
SELECT @user_id::uuid, m.id, m.conversation_id, @key_version, u.ciphertext, @created_at
FROM (SELECT unnest(@message_ids::uuid[]) AS message_id, unnest(@ciphertexts::bytea[]) AS ciphertext) u
JOIN dm_messages m ON m.id = u.message_id AND m.deleted_at IS NULL
JOIN dm_conversations c ON c.id = m.conversation_id AND (c.user_low_id = @user_id::uuid OR c.user_high_id = @user_id::uuid)
ON CONFLICT (user_id, message_id) DO NOTHING;

-- name: ListArchivedMessageIDs :many
SELECT message_id FROM dm_archive_entries
WHERE user_id = @user_id AND message_id = ANY (@message_ids::uuid[]);

-- En yeni önce.
-- name: ListArchiveBefore :many
SELECT message_id, conversation_id, key_version, ciphertext, created_at
FROM dm_archive_entries
WHERE user_id = @user_id AND conversation_id = @conversation_id
  AND (sqlc.narg(before_id)::uuid IS NULL OR message_id < sqlc.narg(before_id)::uuid)
ORDER BY message_id DESC
LIMIT @n;

-- En eski önce, after_id'den yeniler.
-- name: ListArchiveAfter :many
SELECT message_id, conversation_id, key_version, ciphertext, created_at
FROM dm_archive_entries
WHERE user_id = @user_id AND conversation_id = @conversation_id AND message_id > @after_id
ORDER BY message_id
LIMIT @n;

-- name: DeleteArchiveConversation :execrows
DELETE FROM dm_archive_entries WHERE user_id = @user_id AND conversation_id = @conversation_id;

-- name: DeleteUserArchive :execrows
DELETE FROM dm_archive_entries WHERE user_id = @user_id;

-- name: DeleteMessageArchive :exec
DELETE FROM dm_archive_entries WHERE message_id = @message_id;

-- Eşleştirme istekleri.

-- name: DeleteDeviceLinkRequest :exec
DELETE FROM device_link_requests WHERE device_id = @device_id;

-- name: CreateLinkRequest :one
INSERT INTO device_link_requests (id, user_id, device_id, expires_at, created_at)
VALUES (@id, @user_id, @device_id, @expires_at, @created_at)
RETURNING id, expires_at;

-- name: GetLinkRequest :one
SELECT id, user_id, device_id, expires_at, answer, history_key_version, answered_by_device_id, answered_at
FROM device_link_requests
WHERE id = @id AND user_id = @user_id;

-- Yalnızca süresi geçmemiş, cevaplanmamış istek cevaplanır.
-- name: AnswerLinkRequest :execrows
UPDATE device_link_requests
SET answer = @answer, history_key_version = @history_key_version,
    answered_by_device_id = @answered_by_device_id, answered_at = @answered_at
WHERE id = @id AND user_id = @user_id AND answered_at IS NULL AND expires_at > @answered_at;

-- name: DeleteLinkRequest :exec
DELETE FROM device_link_requests WHERE id = @id AND user_id = @user_id;

-- name: PurgeLinkRequests :execrows
DELETE FROM device_link_requests WHERE expires_at < @before;

-- Geçmiş anahtarı paylaşımları.

-- name: InsertHistoryKeyShare :exec
INSERT INTO history_key_shares (id, user_id, recipient_device_id, sender_device_id, history_key_version, bundle, created_at)
VALUES (@id, @user_id, @recipient_device_id, @sender_device_id, @history_key_version, @bundle, @created_at);

-- name: ListHistoryKeyShares :many
SELECT id, sender_device_id, history_key_version, bundle, created_at
FROM history_key_shares
WHERE recipient_device_id = @device_id
ORDER BY id
LIMIT @n;

-- name: AckHistoryKeyShares :execrows
DELETE FROM history_key_shares WHERE recipient_device_id = @device_id AND id = ANY (@ids::uuid[]);

-- name: PurgeHistoryKeyShares :execrows
DELETE FROM history_key_shares WHERE created_at < @before;

-- Hesabı silinen ya da yaptırım alan ve cihazı kalmayan kullanıcının
-- durumu, token'larının süresi çoktan dolduktan sonra (bir gün) silinir.
-- Geçmiş anahtarı sürümü olan (arşivi olan) kullanıcınınki kalır.
-- name: PurgeEndedE2EStates :execrows
DELETE FROM user_e2e_state s
WHERE s.sessions_ended_at < @before AND s.history_key_version = 0
  AND NOT EXISTS (SELECT 1 FROM device_keys d WHERE d.user_id = s.user_id);
