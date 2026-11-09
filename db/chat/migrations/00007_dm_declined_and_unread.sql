-- Birebir mesaj: reddedilenler listesi ve sayaçlı okunmamış bildirim (issue
-- #101; platform sahibi kararları 2026-11-09; veri modeli v14 "Mesaj İsteği
-- Modeli (v7)").
--
-- Reddedilenler: reddedilen gönderen, reddeden onu "reddedilenler"
-- listesinden çıkarana kadar yazamaz (arkadaş olsalar bile). declined_at
-- listenin sırası ve imlecidir; yalnızca declined durumunda doludur.
-- Listeden çıkarılan konuşma request durumuna geri döner (declined_at
-- boşalır): gönderenin tek mesajı yeniden bekleyen istek olur. Gönderen ne
-- reddi ne çıkarılmayı görür.
--
-- Sayaçlı bildirim: konuşma başına okunmamış tek birebir mesaj bildirimi
-- kalır; unread_count alıcının o konuşmadaki okunmamış mesaj sayısıdır
-- (en fazla 100; istemci 100'ü "99+" gösterir). Yalnızca dm_request ve
-- dm_message türlerinde doludur. Her yeni mesajda bekleyen bildirimin sayısı
-- güncellenir; konuşma okununca bildirim okundu olur.
--
-- Geri alma planı: Down sütunları ve kısıtları kaldırır; veri silinmez
-- (reddedilen konuşmalar declined durumunda kalır).

-- +goose Up
ALTER TABLE dm_conversations ADD COLUMN declined_at timestamptz;
-- Önceden reddedilenler: ret zamanı bilinmiyor, en yakın tahmin son
-- güncelleme.
UPDATE dm_conversations SET declined_at = updated_at WHERE status = 'declined';
ALTER TABLE dm_conversations ADD CONSTRAINT dm_conversations_declined_at CHECK ((status = 'declined') = (declined_at IS NOT NULL));
-- Reddedenin listesi (taraflar indeksliyken bile durum ve sıra için).
CREATE INDEX dm_conversations_declined_idx ON dm_conversations (declined_at DESC, id DESC) WHERE status = 'declined';

ALTER TABLE notifications ADD COLUMN unread_count integer;
UPDATE notifications SET unread_count = 1 WHERE type IN ('dm_request', 'dm_message');
ALTER TABLE notifications ADD CONSTRAINT notifications_unread_count CHECK (
  (type IN ('dm_request', 'dm_message')) = (unread_count IS NOT NULL) AND (unread_count IS NULL OR unread_count BETWEEN 1 AND 100)
);

-- +goose Down
ALTER TABLE notifications DROP CONSTRAINT notifications_unread_count;
ALTER TABLE notifications DROP COLUMN unread_count;
DROP INDEX dm_conversations_declined_idx;
ALTER TABLE dm_conversations DROP CONSTRAINT dm_conversations_declined_at;
ALTER TABLE dm_conversations DROP COLUMN declined_at;
