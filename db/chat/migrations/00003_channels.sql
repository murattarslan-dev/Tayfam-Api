-- Tayfa kanalları ve kanal mesajları (veri modeli v14 "Sohbet", "Spam ve
-- taciz", "Silinen içeriği geri alma", "Okunma bilgisi"; ADR-0005, ADR-0009;
-- issue #81).
--
-- tayfa_id, created_by, author_id, deleted_by ve user_id core'un ve
-- identity'nin kimlikleridir; veritabanları arası foreign key yoktur
-- (ADR-0002). Üyelik ve rol her istekte core'a sorulur (GetMembership).
--
-- Mesaj metni yalnızca uygulamada AES-256-GCM ile şifrelenmiş olarak durur
-- (body_encrypted; anahtar CHAT_MESSAGE_KEYS, sürümü key_version). AAD
-- mesaj ve kanal kimliğidir: satır başka bir kanala taşınırsa çözülmez.
--
-- Silme (v14):
--   - Yazar kendi mesajını silerse body_encrypted ve key_version hemen NULL
--     olur; geri alınamaz.
--   - Moderatör veya yönetici silerse şifreli metin kalır, mesaj gizlenir;
--     yönetici 30 gün içinde geri alabilir.
--   - deleted_at'ten 30 gün sonra arka plan işi satırı kalıcı siler.
-- Hesap silinince (user.deleted) author_id, deleted_by ve created_by NULL
-- olur, içerik kalır ("Silinmiş kullanıcı"); okunma kayıtları silinir. Tayfa
-- kalıcı silinince (tayfa.purged) kanallar, mesajları ve okunma kayıtları
-- silinir (CASCADE).
--
-- Geri alma planı: tablolar yenidir; Down onları kaldırır (mesajlar da
-- gider). Uygulamaya açılmadan önce geri alınabilir.

-- +goose Up
CREATE TABLE channels (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL,
  name              text NOT NULL,
  -- strings.ToLowerSpecial(unicode.TurkishCase, name); tekillik bunda.
  name_normalized   text NOT NULL,
  visibility        text NOT NULL DEFAULT 'members',
  slow_mode_seconds integer NOT NULL DEFAULT 0,
  created_by        uuid,
  archived_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT channels_name_len CHECK (char_length(name) BETWEEN 1 AND 40),
  CONSTRAINT channels_name_normalized_len CHECK (char_length(name_normalized) BETWEEN 1 AND 40),
  CONSTRAINT channels_visibility CHECK (visibility IN ('members', 'staff')),
  CONSTRAINT channels_slow_mode CHECK (slow_mode_seconds BETWEEN 0 AND 3600)
);
-- Arşivlenmemiş kanallarda aynı isim olmaz (v14 "İndeksler").
CREATE UNIQUE INDEX channels_name_unique ON channels (tayfa_id, name_normalized) WHERE archived_at IS NULL;
CREATE INDEX channels_tayfa_id_idx ON channels (tayfa_id, id);
CREATE INDEX channels_created_by_idx ON channels (created_by) WHERE created_by IS NOT NULL;
CREATE TRIGGER channels_updated_at BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE channel_messages (
  id                uuid PRIMARY KEY,
  channel_id        uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
  author_id         uuid,
  -- nonce || şifreli metin || etiket. 4000 karakter × 4 bayt + 12 + 16.
  body_encrypted    bytea,
  key_version       smallint,
  -- İstemcinin ürettiği tekrar gönderim anahtarı (ADR-0005); birincil
  -- anahtar her zaman sunucuda üretilir.
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  deleted_by        uuid,

  CONSTRAINT channel_messages_key CHECK ((body_encrypted IS NULL) = (key_version IS NULL)),
  CONSTRAINT channel_messages_key_version CHECK (key_version IS NULL OR key_version BETWEEN 1 AND 255),
  CONSTRAINT channel_messages_body_len CHECK (body_encrypted IS NULL OR octet_length(body_encrypted) BETWEEN 29 AND 16028),
  -- Silinmemiş mesajın metni vardır; silen kişi yalnızca silinmiş mesajda
  -- (hesabı silinince NULL olabilir).
  CONSTRAINT channel_messages_live_body CHECK (deleted_at IS NOT NULL OR body_encrypted IS NOT NULL),
  CONSTRAINT channel_messages_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT channel_messages_once UNIQUE (author_id, client_request_id)
);
-- İmleçli sayfalama (UUIDv7 zaman sıralı) ve okunmamış sayısı.
CREATE INDEX channel_messages_channel_id_idx ON channel_messages (channel_id, id DESC);
-- 30 günlük kalıcı silme ve hesap silmede yazar/silen alanları.
CREATE INDEX channel_messages_deleted_at_idx ON channel_messages (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX channel_messages_deleted_by_idx ON channel_messages (deleted_by) WHERE deleted_by IS NOT NULL;
-- author_id: channel_messages_once'ın indeksi (author_id, client_request_id).

CREATE TABLE channel_reads (
  channel_id           uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
  user_id              uuid NOT NULL,
  last_read_message_id uuid NOT NULL,
  updated_at           timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX channel_reads_user_id_idx ON channel_reads (user_id);
CREATE TRIGGER channel_reads_updated_at BEFORE UPDATE ON channel_reads FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE channel_reads;
DROP TABLE channel_messages;
DROP TABLE channels;
