-- Birebir mesajlar, düz sürüm (issue #95; veri modeli v14 "Sohbet", "Mesaj
-- İsteği Modeli (v7)", "Okunma bilgisi", "İlişkiler ve Silme Davranışı";
-- ADR-0005, ADR-0009).
--
-- Kim kime yazabilir core'a sorulur (DirectMessageService.CheckDirectMessage,
-- kullanıcının kendi token'ıyla); burada yalnızca konuşmanın durumu tutulur.
-- user_low_id, user_high_id, requested_by ve sender_id identity'nin
-- kimlikleridir; veritabanları arası foreign key yoktur (ADR-0002).
--
-- Düz sürüm: mesaj metni kanal mesajlarıyla aynı şekilde, uygulamada
-- AES-256-GCM ile sunucu anahtarıyla şifreli durur (body_encrypted;
-- anahtar CHAT_MESSAGE_KEYS, sürümü key_version; AAD mesaj ve konuşma
-- kimliği). Sunucu içeriği çözebilir; bu, E2E aşamasına kadar bilinçli kabul
-- edilen ara durumdur (platform sahibi kararı, 2026-11-09).
--
-- E2E aşamasına geçiş (ADR-0005 "İki katman"): encryption = 'e2e' olan
-- mesajın body_encrypted ve key_version alanları boş kalır, sender_device_id
-- dolar; içerik dm_envelopes (canlı zarflar) ve dm_archive_entries (geçmiş
-- arşivi) tablolarına gelir. Bu tablolar mesaja message_id ile bağlanacağı
-- için dm_messages'ın şekli değişmez; yalnızca yeni tablolar eklenir.
--
-- Konuşma durumu (v7): request (gönderen yalnızca bir mesaj atabilir),
-- active, declined (alıcı reddetti; gönderen bunu görmez, yeni mesaj
-- atamaz). requested_by yalnızca istek olarak açılan konuşmada doludur:
-- günlük istek sınırı (5/20) buradan, durum değişse de sayılır (reddedilen
-- istek sınırdan düşmez).
--
-- Silme: gönderen kendi mesajını silebilir; metin hemen boşaltılır, geri
-- alınamaz ("Bu mesaj silindi"); deleted_at'ten 30 gün sonra arka plan işi
-- satırı kalıcı siler. Mesaj düzenlenemez.
--
-- Hesap silme (v14 "İlişkiler ve Silme Davranışı": "Kullanıcı → yazdığı
-- içerik SET NULL; dm_conversations tarafları"): silinen kişinin tarafı,
-- requested_by ve sender_id NULL olur, okunma kayıtları silinir. Karşı taraf
-- konuşmayı "Silinmiş kullanıcı" ile görür ama yazamaz. İki tarafı da
-- silinmiş konuşma mesajlarıyla birlikte silinir.
--
-- Bildirim: dm_message türü eklenir (yeni mesaj); dm_request zaten vardı.
-- İkisi de yalnızca konuşma kimliğini taşır, içerik ve gönderen adı taşımaz.
--
-- Geri alma planı: tablolar yenidir; Down onları kaldırır (mesajlar da
-- gider) ve bildirim türünü geri alır (dm_message bildirimleri silinir).

-- +goose Up
CREATE TABLE dm_conversations (
  id              uuid PRIMARY KEY,
  -- Taraflar user_low_id < user_high_id sırasıyla; hesabı silinen taraf NULL.
  user_low_id     uuid,
  user_high_id    uuid,
  status          text NOT NULL,
  -- İstek olarak açan; doğrudan active açılan konuşmada NULL.
  requested_by    uuid,
  accepted_at     timestamptz,
  -- Konuşma listesinin sırası; ilk mesajla birlikte açılır.
  last_message_at timestamptz NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT dm_conversations_status CHECK (status IN ('request', 'active', 'declined')),
  CONSTRAINT dm_conversations_order CHECK (user_low_id IS NULL OR user_high_id IS NULL OR user_low_id < user_high_id),
  CONSTRAINT dm_conversations_requested_by CHECK (requested_by IS NULL OR requested_by = user_low_id OR requested_by = user_high_id),
  -- İstek ve ret yalnızca istek olarak açılmış konuşmada olur.
  CONSTRAINT dm_conversations_request CHECK (status = 'active' OR requested_by IS NOT NULL OR user_low_id IS NULL OR user_high_id IS NULL),
  CONSTRAINT dm_conversations_pair UNIQUE (user_low_id, user_high_id)
);
-- Kişinin konuşmaları (son mesaja göre); user_low_id dm_conversations_pair'in
-- indeksinde.
CREATE INDEX dm_conversations_user_high_id_idx ON dm_conversations (user_high_id);
-- Günlük istek sınırı (v7): son 24 saatte istek olarak açılanlar.
CREATE INDEX dm_conversations_requests_idx ON dm_conversations (requested_by, created_at) WHERE requested_by IS NOT NULL;
CREATE TRIGGER dm_conversations_updated_at BEFORE UPDATE ON dm_conversations FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE dm_messages (
  id                uuid PRIMARY KEY,
  conversation_id   uuid NOT NULL REFERENCES dm_conversations (id) ON DELETE CASCADE,
  sender_id         uuid,
  -- E2E aşamasında gönderen cihaz; düz sürümde NULL.
  sender_device_id  uuid,
  -- server: içerik body_encrypted'da, sunucu anahtarıyla (düz sürüm).
  -- e2e: içerik zarflarda ve arşivde; body_encrypted hep NULL.
  encryption        text NOT NULL DEFAULT 'server',
  -- nonce || şifreli metin || etiket. 4000 karakter × 4 bayt + 12 + 16.
  body_encrypted    bytea,
  key_version       smallint,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,

  CONSTRAINT dm_messages_encryption CHECK (encryption IN ('server', 'e2e')),
  CONSTRAINT dm_messages_key CHECK ((body_encrypted IS NULL) = (key_version IS NULL)),
  CONSTRAINT dm_messages_key_version CHECK (key_version IS NULL OR key_version BETWEEN 1 AND 255),
  CONSTRAINT dm_messages_body_len CHECK (body_encrypted IS NULL OR octet_length(body_encrypted) BETWEEN 29 AND 16028),
  -- Sunucu şifreli canlı mesajın metni vardır; silinmişin yoktur. E2E
  -- mesajın sunucuda metni hiç olmaz.
  CONSTRAINT dm_messages_body CHECK (
    (encryption = 'server' AND (deleted_at IS NULL) = (body_encrypted IS NOT NULL)) OR
    (encryption = 'e2e' AND body_encrypted IS NULL)
  ),
  CONSTRAINT dm_messages_device CHECK (encryption = 'e2e' OR sender_device_id IS NULL),
  CONSTRAINT dm_messages_once UNIQUE (sender_id, client_request_id)
);
-- İmleçli sayfalama (UUIDv7 zaman sıralı) ve okunmamış sayısı.
CREATE INDEX dm_messages_conversation_id_idx ON dm_messages (conversation_id, id DESC);
-- 30 günlük kalıcı silme.
CREATE INDEX dm_messages_deleted_at_idx ON dm_messages (deleted_at) WHERE deleted_at IS NOT NULL;
-- sender_id: dm_messages_once'ın indeksi (sender_id, client_request_id).

CREATE TABLE dm_conversation_reads (
  conversation_id      uuid NOT NULL REFERENCES dm_conversations (id) ON DELETE CASCADE,
  user_id              uuid NOT NULL,
  last_read_message_id uuid NOT NULL,
  read_at              timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX dm_conversation_reads_user_id_idx ON dm_conversation_reads (user_id);

ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived'
));
-- Konuşma başına okunmamış tek "yeni mesaj" bildirimi (yinelenmesin diye
-- aranır) ve konuşma okununca bildirimlerin okundu işareti.
CREATE INDEX notifications_dm_idx ON notifications (user_id, target_id) WHERE target_type = 'dm_conversation' AND read_at IS NULL;

-- +goose Down
DROP INDEX notifications_dm_idx;
DELETE FROM notifications WHERE type = 'dm_message';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived'
));
DROP TABLE dm_conversation_reads;
DROP TABLE dm_messages;
DROP TABLE dm_conversations;
