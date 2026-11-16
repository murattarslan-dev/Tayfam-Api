-- Sohbette fotoğraf ve yer (issue #205, ADR-0031).
--
-- Kanal mesajının türü: text (bugünkü mesajlar), photo, place.
--   - photo: photo_upload_id core'daki dosyanın kimliğidir (servisler arası
--     FK yok, ADR-0002); metni yoktur. Yazar silince kimlik hemen boşalır
--     ve dosya chat.media_released olayıyla core'da silinir; moderatör
--     gizlemesi dosyayı tutar (30 gün geri alınabilir).
--   - place: {enlem, boylam, ad} metin gibi sunucu anahtarıyla şifrelidir
--     (body_encrypted); veritabanı açık koordinatı görmez, aralık denetimi
--     Go'dadır.
--
-- Birebir mesaj: türü sunucuda yoktur (zarfın içinde). Yalnızca, varsa,
-- istemcinin şifrelediği ekin dosya kimliği (attachment_upload_id); anahtar
-- ve özet zarfın içindedir. Gönderen silince kimlik hemen boşalır.
--
-- Geri alma planı: Down fotoğraf ve yer mesajlarını siler (eski şemada
-- karşılıkları yok; dosyaları core'da 7 günlük bağlanmamış temizliğiyle
-- değil, core'un kendi Down'ıyla gider), kolonları düşürür.

-- +goose Up
ALTER TABLE channel_messages
  ADD COLUMN kind text NOT NULL DEFAULT 'text',
  ADD COLUMN photo_upload_id uuid;

ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_kind CHECK (kind IN ('text', 'photo', 'place'));
-- Fotoğraf mesajının metni yoktur; dosya kimliği yalnızca fotoğraf
-- mesajındadır.
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_photo CHECK (
  (kind = 'photo' OR photo_upload_id IS NULL)
  AND (kind <> 'photo' OR body_encrypted IS NULL));
-- Silinmemiş mesajın içeriği vardır: metin ve yer şifreli gövdede, fotoğraf
-- dosya kimliğinde.
ALTER TABLE channel_messages DROP CONSTRAINT channel_messages_live_body;
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_live_body CHECK (
  deleted_at IS NOT NULL
  OR (kind = 'photo' AND photo_upload_id IS NOT NULL)
  OR (kind <> 'photo' AND body_encrypted IS NOT NULL));
-- Bir dosya tek mesajdadır.
CREATE UNIQUE INDEX channel_messages_photo_upload_id_key ON channel_messages (photo_upload_id)
  WHERE photo_upload_id IS NOT NULL;

ALTER TABLE dm_messages ADD COLUMN attachment_upload_id uuid;
-- Silinen mesajın eki yoktur (gönderen silince hemen boşalır).
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_attachment CHECK (deleted_at IS NULL OR attachment_upload_id IS NULL);
CREATE UNIQUE INDEX dm_messages_attachment_upload_id_key ON dm_messages (attachment_upload_id)
  WHERE attachment_upload_id IS NOT NULL;

-- chat_app (ADR-0015): channel_messages'ta zaten tablo düzeyinde UPDATE
-- vardır (00009); birebirde yalnızca ekin boşaltılması.
GRANT UPDATE (attachment_upload_id) ON dm_messages TO chat_app;

-- +goose Down
REVOKE UPDATE (attachment_upload_id) ON dm_messages FROM chat_app;
DROP INDEX dm_messages_attachment_upload_id_key;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_attachment;
ALTER TABLE dm_messages DROP COLUMN attachment_upload_id;

DELETE FROM channel_messages WHERE kind <> 'text';
DROP INDEX channel_messages_photo_upload_id_key;
ALTER TABLE channel_messages DROP CONSTRAINT channel_messages_live_body;
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_live_body CHECK (deleted_at IS NOT NULL OR body_encrypted IS NOT NULL);
ALTER TABLE channel_messages DROP CONSTRAINT channel_messages_photo;
ALTER TABLE channel_messages DROP CONSTRAINT channel_messages_kind;
ALTER TABLE channel_messages
  DROP COLUMN photo_upload_id,
  DROP COLUMN kind;
