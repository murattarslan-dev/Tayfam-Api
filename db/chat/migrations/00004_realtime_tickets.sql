-- Gerçek zamanlı bağlantı biletleri (ADR-0004 "WebSocket kimlik
-- doğrulaması", ADR-0005 "Bağlantı", ADR-0009 "Bağlantı: realtime_tickets";
-- issue #82).
--
-- İstemci erişim token'ıyla bilet alır, WebSocket'i biletle açar; böylece
-- token URL'de taşınmaz. Bilet 30 saniye geçerli ve tek kullanımlıktır:
-- kullanılınca satır silinir (DELETE … RETURNING), süresi dolanları arka
-- plan işi siler. Biletin kendisi saklanmaz, yalnızca SHA-256 özeti
-- tutulur: veritabanını okuyan biri bağlantı açamaz.
--
-- session_id, oturum iptal edilince (session.revoked) o oturumun
-- biletlerini ve bağlantılarını bulmak içindir.

-- +goose Up
CREATE TABLE realtime_tickets (
  id         uuid PRIMARY KEY,
  token_hash bytea NOT NULL,
  user_id    uuid NOT NULL,
  session_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,

  CONSTRAINT realtime_tickets_token_hash_unique UNIQUE (token_hash),
  CONSTRAINT realtime_tickets_token_hash_len CHECK (octet_length(token_hash) = 32),
  -- Kısa ömürlü: en fazla 1 dakika (uygulama 30 saniye verir).
  CONSTRAINT realtime_tickets_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '1 minute')
);

CREATE INDEX realtime_tickets_user ON realtime_tickets (user_id, expires_at);
CREATE INDEX realtime_tickets_session ON realtime_tickets (session_id);
CREATE INDEX realtime_tickets_expires ON realtime_tickets (expires_at);

-- +goose Down
DROP TABLE realtime_tickets;
