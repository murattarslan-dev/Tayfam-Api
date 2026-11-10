-- Birebir mesajda uçtan uca şifreleme, sunucu tarafı (issue #127;
-- ADR-0018, ADR-0005 "Direkt mesajlar", ADR-0009; veri modeli v14
-- "Sohbet", "E2E güvenlik kodu uyarısı", "Saklama süreleri").
--
-- Sunucu hiçbir kriptografi yapmaz: açık anahtarları, opak zarfları,
-- kullanıcının geçmiş anahtarıyla şifreli arşivi ve eşleştirme mesajlarını
-- boyut sınırıyla saklar ve iletir. Bu tablolardaki bytea alanların içeriği
-- log'a, olaya ve hata mesajına girmez.
--
-- Cihaz = oturum (ADR-0018 madde 1): device_keys.session_id, cihazı
-- kaydeden erişim token'ının sid'idir; oturum bitince (session.revoked,
-- user.sanctioned, user.deleted) cihaz silinir. user_id ve session_id
-- identity'nin kimlikleridir; veritabanları arası foreign key yoktur
-- (ADR-0002).
--
-- v14'ten farklar (ADR-0018 madde 2): imzalı ön anahtar ve son çare Kyber
-- ön anahtarı device_keys satırındadır (device_signed_prekeys tablosu
-- yok); tek kullanımlık ön anahtar consumed_at ile işaretlenmez, verilince
-- silinir.
--
-- Biçim denetimleri (uzunluk, Signal anahtar türü baytı 0x05) yalnızca
-- biçimdir; imza doğrulanmaz.
--
-- Geri alma planı: tablolar yenidir; Down onları kaldırır (cihazlar,
-- bekleyen zarflar ve arşiv gider; dm_messages'taki e2e satırları kalır ve
-- içeriksiz görünür).

-- +goose Up

-- Kullanıcı başına E2E durumu: geçmiş anahtarının sürümü (anahtarın
-- kendisi asla), yenileme gerekiyor mu, son verilen cihaz numarası ve
-- yaptırımda token'ların geçersiz sayıldığı an.
CREATE TABLE user_e2e_state (
  user_id             uuid PRIMARY KEY,
  history_key_version smallint NOT NULL DEFAULT 0,
  rotation_needed     boolean NOT NULL DEFAULT false,
  last_device_number  smallint NOT NULL DEFAULT 0,
  sessions_ended_at   timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_e2e_state_version CHECK (history_key_version BETWEEN 0 AND 32767),
  CONSTRAINT user_e2e_state_device_number CHECK (last_device_number BETWEEN 0 AND 127)
);
CREATE TRIGGER user_e2e_state_updated_at BEFORE UPDATE ON user_e2e_state FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Kayıtlı E2E cihazları (v13 devices'ın anahtar alanları).
CREATE TABLE device_keys (
  id                      uuid PRIMARY KEY,
  user_id                 uuid NOT NULL,
  session_id              uuid NOT NULL,
  device_number           smallint NOT NULL,
  registration_id         integer NOT NULL,
  identity_key            bytea NOT NULL,
  signed_prekey_id        integer NOT NULL,
  signed_prekey           bytea NOT NULL,
  signed_prekey_signature bytea NOT NULL,
  -- Son çare Kyber ön anahtarı (PQXDH): tek kullanımlık Kyber kalmadığında
  -- pakete bu konur.
  kyber_prekey_id         integer NOT NULL,
  kyber_prekey            bytea NOT NULL,
  kyber_prekey_signature  bytea NOT NULL,
  -- Geçmiş anahtarını biliyor (ADR-0005 "eşleştirilmiş cihaz").
  paired_at               timestamptz,
  created_at              timestamptz NOT NULL DEFAULT now(),
  prekeys_updated_at      timestamptz NOT NULL DEFAULT now(),
  last_seen_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT device_keys_session UNIQUE (session_id),
  CONSTRAINT device_keys_number UNIQUE (user_id, device_number),
  CONSTRAINT device_keys_device_number CHECK (device_number BETWEEN 1 AND 127),
  CONSTRAINT device_keys_registration_id CHECK (registration_id BETWEEN 1 AND 16380),
  CONSTRAINT device_keys_identity_key CHECK (octet_length(identity_key) = 33 AND get_byte(identity_key, 0) = 5),
  CONSTRAINT device_keys_signed_prekey_id CHECK (signed_prekey_id BETWEEN 0 AND 16777215),
  CONSTRAINT device_keys_signed_prekey CHECK (octet_length(signed_prekey) = 33 AND get_byte(signed_prekey, 0) = 5),
  CONSTRAINT device_keys_signed_prekey_signature CHECK (octet_length(signed_prekey_signature) = 64),
  CONSTRAINT device_keys_kyber_prekey_id CHECK (kyber_prekey_id BETWEEN 0 AND 16777215),
  CONSTRAINT device_keys_kyber_prekey CHECK (octet_length(kyber_prekey) = 1569),
  CONSTRAINT device_keys_kyber_prekey_signature CHECK (octet_length(kyber_prekey_signature) = 64)
);
-- user_id: device_keys_number'ın indeksi (user_id, device_number).
CREATE INDEX device_keys_last_seen_at_idx ON device_keys (last_seen_at);

-- Tek kullanımlık ön anahtarlar: ec (X3DH) ve kyber (PQXDH). Paket
-- çekilirken FOR UPDATE SKIP LOCKED ile seçilip silinir; bir anahtar iki
-- kişiye verilmez.
CREATE TABLE device_one_time_prekeys (
  device_id  uuid NOT NULL REFERENCES device_keys (id) ON DELETE CASCADE,
  kind       text NOT NULL,
  key_id     integer NOT NULL,
  public_key bytea NOT NULL,
  -- Yalnızca kyber: kimlik anahtarıyla imza.
  signature  bytea,
  created_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (device_id, kind, key_id),
  CONSTRAINT device_one_time_prekeys_kind CHECK (kind IN ('ec', 'kyber')),
  CONSTRAINT device_one_time_prekeys_key_id CHECK (key_id BETWEEN 0 AND 16777215),
  CONSTRAINT device_one_time_prekeys_key CHECK (
    (kind = 'ec' AND octet_length(public_key) = 33 AND get_byte(public_key, 0) = 5 AND signature IS NULL) OR
    (kind = 'kyber' AND octet_length(public_key) = 1569 AND octet_length(signature) = 64)
  )
);

-- "Güvenlik kodu değişti" uyarısının kaynağı (v14). device_id bilerek
-- foreign key değildir: kaldırılan cihazın olayı kalır. 90 gün tutulur.
CREATE TABLE user_key_events (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL,
  device_id     uuid NOT NULL,
  device_number smallint NOT NULL,
  event_type    text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_key_events_type CHECK (event_type IN ('device_added', 'device_removed', 'identity_changed')),
  CONSTRAINT user_key_events_device_number CHECK (device_number BETWEEN 1 AND 127)
);
CREATE INDEX user_key_events_user_id_idx ON user_key_events (user_id, id DESC);
CREATE INDEX user_key_events_created_at_idx ON user_key_events (created_at);

-- Biten oturumlar: erişim token'ı hâlâ geçerli olsa da (≤ 15 dk) yeni
-- cihaz kaydedemez. Bir gün tutulur.
CREATE TABLE e2e_ended_sessions (
  session_id uuid PRIMARY KEY,
  ended_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX e2e_ended_sessions_ended_at_idx ON e2e_ended_sessions (ended_at);

-- Zarflar (canlı iletim): her hedef cihaz için ayrı opak Signal mesajı.
-- Teslim onayında, mesaj silinince, cihaz kaldırılınca ya da 30 gün sonra
-- silinir. message_type Signal'in CiphertextMessageType'ıdır: 2 whisper,
-- 3 prekey.
CREATE TABLE dm_envelopes (
  id                   uuid PRIMARY KEY,
  message_id           uuid NOT NULL REFERENCES dm_messages (id) ON DELETE CASCADE,
  recipient_device_id  uuid NOT NULL REFERENCES device_keys (id) ON DELETE CASCADE,
  sender_id            uuid NOT NULL,
  sender_device_id     uuid NOT NULL,
  sender_device_number smallint NOT NULL,
  message_type         smallint NOT NULL,
  ciphertext           bytea NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT dm_envelopes_once UNIQUE (message_id, recipient_device_id),
  CONSTRAINT dm_envelopes_type CHECK (message_type IN (2, 3)),
  CONSTRAINT dm_envelopes_sender_device_number CHECK (sender_device_number BETWEEN 1 AND 127),
  CONSTRAINT dm_envelopes_ciphertext CHECK (octet_length(ciphertext) BETWEEN 1 AND 65536)
);
CREATE INDEX dm_envelopes_recipient_idx ON dm_envelopes (recipient_device_id, id);
CREATE INDEX dm_envelopes_created_at_idx ON dm_envelopes (created_at);
CREATE INDEX dm_envelopes_sender_id_idx ON dm_envelopes (sender_id);
-- message_id: dm_envelopes_once'ın indeksi.

-- Geçmiş arşivi: kullanıcının geçmiş anahtarıyla şifreli kayıt, (kullanıcı,
-- mesaj) başına bir. Sunucu çözemez.
CREATE TABLE dm_archive_entries (
  user_id         uuid NOT NULL,
  message_id      uuid NOT NULL REFERENCES dm_messages (id) ON DELETE CASCADE,
  conversation_id uuid NOT NULL REFERENCES dm_conversations (id) ON DELETE CASCADE,
  key_version     smallint NOT NULL,
  ciphertext      bytea NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (user_id, message_id),
  CONSTRAINT dm_archive_entries_key_version CHECK (key_version BETWEEN 1 AND 32767),
  CONSTRAINT dm_archive_entries_ciphertext CHECK (octet_length(ciphertext) BETWEEN 1 AND 32768)
);
CREATE INDEX dm_archive_entries_conversation_idx ON dm_archive_entries (user_id, conversation_id, message_id DESC);
CREATE INDEX dm_archive_entries_message_id_idx ON dm_archive_entries (message_id);
CREATE INDEX dm_archive_entries_conversation_id_idx ON dm_archive_entries (conversation_id);

-- Eşleştirme (QR) istekleri: yeni cihaz açar, aynı kullanıcının
-- eşleştirilmiş cihazı geçmiş anahtarlarını yeni cihazın açık anahtarıyla
-- şifreleyip cevaplar (answer, opak). 10 dakika yaşar; cihaz başına bir.
CREATE TABLE device_link_requests (
  id                    uuid PRIMARY KEY,
  user_id               uuid NOT NULL,
  device_id             uuid NOT NULL REFERENCES device_keys (id) ON DELETE CASCADE,
  expires_at            timestamptz NOT NULL,
  answer                bytea,
  history_key_version   smallint,
  answered_by_device_id uuid,
  answered_at           timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT device_link_requests_device UNIQUE (device_id),
  CONSTRAINT device_link_requests_expiry CHECK (expires_at > created_at),
  CONSTRAINT device_link_requests_answer CHECK (
    (answer IS NULL) = (answered_at IS NULL) AND (answer IS NULL) = (history_key_version IS NULL) AND
    (answer IS NULL) = (answered_by_device_id IS NULL)
  ),
  CONSTRAINT device_link_requests_answer_len CHECK (answer IS NULL OR octet_length(answer) BETWEEN 1 AND 16384),
  CONSTRAINT device_link_requests_version CHECK (history_key_version IS NULL OR history_key_version BETWEEN 1 AND 32767)
);
CREATE INDEX device_link_requests_expires_at_idx ON device_link_requests (expires_at);

-- Geçmiş anahtarının yenilenmesinde eşleştirilmiş cihazlara paylaşım
-- (opak). Onayda ya da 30 gün sonra silinir.
CREATE TABLE history_key_shares (
  id                  uuid PRIMARY KEY,
  user_id             uuid NOT NULL,
  recipient_device_id uuid NOT NULL REFERENCES device_keys (id) ON DELETE CASCADE,
  sender_device_id    uuid NOT NULL,
  history_key_version smallint NOT NULL,
  bundle              bytea NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT history_key_shares_version CHECK (history_key_version BETWEEN 1 AND 32767),
  CONSTRAINT history_key_shares_bundle CHECK (octet_length(bundle) BETWEEN 1 AND 16384)
);
CREATE INDEX history_key_shares_recipient_idx ON history_key_shares (recipient_device_id, id);
CREATE INDEX history_key_shares_created_at_idx ON history_key_shares (created_at);

-- chat_app (ADR-0015): 00009'daki varsayılan yetkiyle her yeni tablo
-- SELECT, INSERT alır. Ek olarak yalnızca sorguların ve saklama işinin
-- gerektirdiği DELETE ve kolon düzeyinde UPDATE verilir. Kimlik anahtarı,
-- kayıt kimliği, oturum, kullanıcı ve cihaz numarası hiçbir zaman
-- güncellenemez; zarf, arşiv kaydı, anahtar olayı ve paylaşım düzenlenemez.
-- Tek kullanımlık ön anahtarın satır kilidi (FOR UPDATE) en az bir kolonda
-- UPDATE ister: created_at verilir (anahtar materyali değişemez).
GRANT DELETE ON
  user_e2e_state, device_keys, device_one_time_prekeys, user_key_events, e2e_ended_sessions,
  dm_envelopes, dm_archive_entries, device_link_requests, history_key_shares
  TO chat_app;
GRANT UPDATE (history_key_version, rotation_needed, last_device_number, sessions_ended_at)
  ON user_e2e_state TO chat_app;
GRANT UPDATE (signed_prekey_id, signed_prekey, signed_prekey_signature, kyber_prekey_id, kyber_prekey,
              kyber_prekey_signature, paired_at, prekeys_updated_at, last_seen_at)
  ON device_keys TO chat_app;
GRANT UPDATE (created_at) ON device_one_time_prekeys TO chat_app;
GRANT UPDATE (answer, history_key_version, answered_by_device_id, answered_at)
  ON device_link_requests TO chat_app;

-- +goose Down
DROP TABLE history_key_shares;
DROP TABLE device_link_requests;
DROP TABLE dm_archive_entries;
DROP TABLE dm_envelopes;
DROP TABLE e2e_ended_sessions;
DROP TABLE user_key_events;
DROP TABLE device_one_time_prekeys;
DROP TABLE device_keys;
DROP TABLE user_e2e_state;
