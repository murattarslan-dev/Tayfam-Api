-- Birebir mesaj yalnızca uçtan uca şifreli (issue #151, ADR-0018 §7).
-- Platform sahibinin kararı (2026-11-11): istemcinin E2E'li sürümüyle düz
-- metinli gönderim yolu kalkar; düz sürümde yazılmış (encryption =
-- 'server', CHAT_MESSAGE_KEYS ile şifreli) birebir mesajlar silinir. Pilot
-- öncesidir, gerçek kullanıcı verisi yoktur.
--
-- Silinen mesajlara bağlı zarf ve arşiv satırları zaten yoktur (yalnızca
-- e2e mesajlarda olurlar; ON DELETE CASCADE yine de korur). Okundu
-- işaretleri (dm_conversation_reads.last_read_message_id) mesaja foreign
-- key ile bağlı değildir; silinen bir mesajı göstermeye devam eden işaret
-- sayfalamada yalnızca bir sınırdır. Konuşmalar ve istek durumları kalır.
--
-- Sunucu artık birebir mesaj metni tutmaz: body_encrypted ve key_version
-- kolonları düşer, encryption yalnızca 'e2e' olabilir. Bir bileşen
-- ele geçirilse de sunucuya düz metin birebir mesaj yazamaz.
--
-- Geri alma planı: Down kolonları ve eski kısıtları geri koyar; silinen
-- mesajlar geri gelmez (bilerek: pilot öncesi test verisi).

-- +goose Up
DELETE FROM dm_messages WHERE encryption <> 'e2e';
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_body;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_body_len;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_key_version;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_key;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_device;
-- Kolon düşünce chat_app'in o kolonlardaki UPDATE yetkisi de gider.
ALTER TABLE dm_messages DROP COLUMN body_encrypted;
ALTER TABLE dm_messages DROP COLUMN key_version;
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_encryption;
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_encryption CHECK (encryption = 'e2e');
ALTER TABLE dm_messages ALTER COLUMN encryption SET DEFAULT 'e2e';

-- +goose Down
ALTER TABLE dm_messages ALTER COLUMN encryption SET DEFAULT 'server';
ALTER TABLE dm_messages DROP CONSTRAINT dm_messages_encryption;
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_encryption CHECK (encryption IN ('server', 'e2e'));
ALTER TABLE dm_messages ADD COLUMN body_encrypted bytea;
ALTER TABLE dm_messages ADD COLUMN key_version smallint;
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_key CHECK ((body_encrypted IS NULL) = (key_version IS NULL));
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_key_version CHECK (key_version IS NULL OR key_version BETWEEN 1 AND 255);
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_body_len CHECK (body_encrypted IS NULL OR octet_length(body_encrypted) BETWEEN 29 AND 16028);
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_body CHECK (
  (encryption = 'server' AND (deleted_at IS NULL) = (body_encrypted IS NOT NULL)) OR
  (encryption = 'e2e' AND body_encrypted IS NULL)
);
ALTER TABLE dm_messages ADD CONSTRAINT dm_messages_device CHECK (encryption = 'e2e' OR sender_device_id IS NULL);
GRANT UPDATE (body_encrypted, key_version) ON dm_messages TO chat_app;
