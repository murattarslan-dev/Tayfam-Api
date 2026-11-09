-- chat veritabanı: temel tablolar (ADR-0002, ADR-0009, issue #64).
-- ID'ler UUIDv7 olarak uygulamada üretilir; zamanlar timestamptz (UTC).
-- Kullanıcı ve tayfa kimlikleri identity'nin ve core'un verisidir;
-- veritabanları arası foreign key olmadığı için bu kolonlar referans değildir.
-- Silinen kullanıcı identity'nin user.deleted olayıyla temizlenir (ADR-0009).
--
-- Bu migration yalnızca her serviste bulunan tabloları açar: outbox
-- (domain_events, event_consumers) ve kullanıcı profili kopyası
-- (user_profiles). Kanal, mesaj ve bildirim tabloları kendi iş paketleriyle
-- gelir.

-- +goose Up

-- Başka servislerin kullanıcıları bu veritabanına bağlanamaz; şemada da
-- varsayılan bir yetkileri yoktur (ADR-0002, ADR-0011).
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END
$$;
-- +goose StatementEnd
REVOKE ALL ON SCHEMA public FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Outbox (ADR-0002, veri modeli v10). identity ve core'dakiyle aynı ortak
-- kolonlar; olaylar internal/platform/outbox.Write ile yazılır (sıralama
-- kilidi). chat şu an olay yayınlamaz; tablolar her serviste bulunur
-- (ADR-0009) ve chat'in ileride yayınlayacağı olaylar (ör. mesaj şikâyeti)
-- buraya yazılır. payload'a kişisel içerik (mesaj metni, konum) yazılmaz.
-- ---------------------------------------------------------------------------

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

CREATE TABLE event_consumers (
  consumer_name text PRIMARY KEY,
  last_event_id uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_consumers_name_format CHECK (consumer_name ~ '^[a-z][a-z0-9_.-]{0,62}$')
);
CREATE TRIGGER event_consumers_updated_at BEFORE UPDATE ON event_consumers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Kullanıcı profili kopyası (ADR-0009): chat, listelerde (kanal mesajı,
-- bildirim) yazar adını ve avatarını her istekte identity'ye sormadan
-- gösterir. Kopya yalnızca identity'nin user.profile_updated ve user.deleted
-- olaylarıyla değişir; kısa süre eski kalması kabul edilir. core'daki
-- kopyayla aynı biçimdedir:
--   source_event_id: son uygulanan identity olayı. Olaylar en az bir kez ve
--     sırası bozulabilir biçimde gelir; daha eski ya da aynı olay kopyayı
--     değiştirmez (idempotent tüketici, ADR-0002).
--   deleted_at: hesap silindi. Ad ve avatar temizlenir, satır mezar taşı
--     olarak kalır; geç gelen bir profil olayı silinen profili geri getiremez.
-- ---------------------------------------------------------------------------

CREATE TABLE user_profiles (
  user_id          uuid PRIMARY KEY,
  display_name     text,
  avatar_upload_id uuid,
  source_event_id  uuid NOT NULL,
  deleted_at       timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_profiles_display_name_len CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 40),
  -- Silinmemiş profilin adı vardır; silinmiş profilde kişisel veri kalmaz.
  CONSTRAINT user_profiles_deleted CHECK (
    (deleted_at IS NULL AND display_name IS NOT NULL) OR
    (deleted_at IS NOT NULL AND display_name IS NULL AND avatar_upload_id IS NULL)
  )
);

-- +goose Down
DROP TABLE user_profiles;
DROP TABLE event_consumers;
DROP TABLE domain_events;
DROP FUNCTION set_updated_at();
