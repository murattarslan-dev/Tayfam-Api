-- Outbox (ADR-0002, ADR-0009, veri modeli v10): identity'nin olayları
-- (user.profile_updated, user.deleted) iş verisiyle aynı transaction'da
-- yazılır; relay hedef servislere (core, ileride chat) iletir ve her hedefin
-- konumunu event_consumers'ta tutar. Tablolar core'dakiyle aynıdır; tayfa_id
-- identity'de yoktur. Olaylar internal/platform/outbox.Write ile yazılır
-- (sıralama için kilit, outbox paket belgesi).
--
-- payload'a kişisel içerik yazılmaz. Tek istisna profil kopyasıdır
-- (ADR-0009): user.profile_updated görünen adı ve avatar kimliğini taşır.
-- Hesap silinince bu olaylar silinir ve actor_id NULL olur.

-- +goose Up
CREATE TABLE domain_events (
  id           uuid PRIMARY KEY,
  type         text NOT NULL,
  actor_id     uuid,
  subject_type text,
  subject_id   uuid,
  payload      jsonb NOT NULL DEFAULT '{}',
  occurred_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT domain_events_type_format CHECK (type ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND char_length(type) <= 64),
  CONSTRAINT domain_events_subject_type_format CHECK (subject_type IS NULL OR (subject_type ~ '^[a-z][a-z_]*$' AND char_length(subject_type) <= 32)),
  CONSTRAINT domain_events_subject CHECK ((subject_type IS NULL) = (subject_id IS NULL)),
  CONSTRAINT domain_events_payload CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 8192)
);
CREATE INDEX domain_events_type_id_idx ON domain_events (type, id);
CREATE INDEX domain_events_actor_id_idx ON domain_events (actor_id, id) WHERE actor_id IS NOT NULL;
-- Hesap silmede kullanıcının eski profil olaylarını bulmak için.
CREATE INDEX domain_events_subject_idx ON domain_events (subject_type, subject_id) WHERE subject_id IS NOT NULL;

CREATE TABLE event_consumers (
  consumer_name text PRIMARY KEY,
  last_event_id uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_consumers_name_format CHECK (consumer_name ~ '^[a-z][a-z0-9_.-]{0,62}$')
);
CREATE TRIGGER event_consumers_updated_at BEFORE UPDATE ON event_consumers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE event_consumers;
DROP TABLE domain_events;
