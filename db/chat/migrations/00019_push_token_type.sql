-- Push token türü (issue #230, ADR-0036): firebase_messaging 17+ istemcileri
-- FCM kayıt token'ı yerine Firebase Installation ID (FID) kaydeder. FCM HTTP
-- v1 ikisini ayrı alanlarla hedefler (message.token, message.fid); chat
-- gönderimde türe bakar.
--
-- Mevcut satırlar eski istemcilerin kayıt token'larıdır: varsayılan
-- 'registration_token' onları olduğu gibi bırakır. Token değeri ve biçim
-- kısıtı (push_tokens_token) değişmez; FID aynı alfabeye uyar. UNIQUE (token)
-- da değişmez: aynı değer yalnızca bir oturumda durur.
--
-- chat_app yalnızca kaydı yenilerken türü de yazabilir (UPDATE kolonu).
--
-- Geri alma planı: Down FID satırlarını siler (eski kod onları kayıt token'ı
-- olarak gönderirdi; FCM geçiş döneminde message.token'da FID'i kabul etse de
-- tür bilgisi olmadan tutulmaz) ve kolonu kaldırır. İstemciler açılışta
-- yeniden kaydeder.

-- +goose Up
ALTER TABLE push_tokens
  ADD COLUMN token_type text NOT NULL DEFAULT 'registration_token',
  ADD CONSTRAINT push_tokens_token_type CHECK (token_type IN ('registration_token', 'fid'));

GRANT UPDATE (token_type) ON push_tokens TO chat_app;

-- +goose Down
REVOKE UPDATE (token_type) ON push_tokens FROM chat_app;
DELETE FROM push_tokens WHERE token_type = 'fid';
ALTER TABLE push_tokens DROP COLUMN token_type;
