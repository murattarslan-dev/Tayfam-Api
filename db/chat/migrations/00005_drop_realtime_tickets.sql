-- Gerçek zamanlı bağlantı biletleri kaldırıldı (issue #82, 2. adım; ADR-0005
-- "Bağlantı", 2026-11-09 revizyonu). WebSocket artık kullanıcının erişim
-- token'ıyla açılır: mobilde Authorization başlığı, web'de HttpOnly çerez;
-- token URL'de taşınmaz. Bilet gereksiz kaldığı için tablo silinir. İçindeki
-- veri en fazla 30 saniyelik, kullanılmamış bilet özetleridir.

-- +goose Up
DROP TABLE realtime_tickets;

-- +goose Down
CREATE TABLE realtime_tickets (
  id         uuid PRIMARY KEY,
  token_hash bytea NOT NULL,
  user_id    uuid NOT NULL,
  session_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,

  CONSTRAINT realtime_tickets_token_hash_unique UNIQUE (token_hash),
  CONSTRAINT realtime_tickets_token_hash_len CHECK (octet_length(token_hash) = 32),
  CONSTRAINT realtime_tickets_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '1 minute')
);

CREATE INDEX realtime_tickets_user ON realtime_tickets (user_id, expires_at);
CREATE INDEX realtime_tickets_session ON realtime_tickets (session_id);
CREATE INDEX realtime_tickets_expires ON realtime_tickets (expires_at);
