-- Etkinlik türü kataloğu (issue #237; ADR-0025 §1–§3, §8; ADR-0037 §1–§3;
-- ADR-0041; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri (2026-11-18)").
--
-- Tür artık veridir: ad, ikon, ilgi alanları, mod, alanlar, bileşenler ve
-- parametreleri bu tablolardadır. Bileşen koddur (core + istemci).
--
--   catalog_interests            türleri gruplayan başlıklar
--   catalog_event_types          türlerin güncel hali (version, definition)
--   catalog_event_type_versions  her tür sürümünün değişmez kopyası;
--                                etkinlik oluşturulduğu sürüme bağlanır
--   catalog_meta                 tek satır: kataloğun bütününün sürümü
--                                (GET /v1/catalog'un ETag'i)
--
-- Kurallar (ADR-0025 §2):
--   * Anahtar değişmez, kayıt silinmez. Kaldırılan tür ya da ilgi alanı
--     status = 'retired' olur.
--   * Her değişiklik türün version'ını ve catalog_meta.catalog_version'ı
--     artırır; eski sürüm satırı değişmez.
--   * definition Go'da kapalı şemaya karşı doğrulanır
--     (internal/core/app/catalog/definition): bilinmeyen anahtar reddedilir,
--     16 KiB sınırı vardır. Buradaki CHECK'ler ikinci savunma hattıdır;
--     jsonb metni boşluk eklediği için boyut sınırı burada gevşektir.
--
-- Yetki (ADR-0015, en az yetki): core_app bu tabloları yalnızca OKUR.
-- Katalog şimdilik yalnızca migration'la değişir; platform paneli (#262)
-- kendi migration'ında gereken yazma yetkisini açar (sürüm tablosunda
-- UPDATE ve DELETE açılmamalı: sürüm satırı değişmez).
--
-- Seed: Faz 1'in yedi türü, bugünkü davranışlarıyla (ADR-0025 §10 madde 1).
-- Anahtarlar değişmez; "sports" artık "Spor (diğer)", "gaming" artık "Video
-- oyunu"dur. Tam katalog (≈70 tür, 15 ilgi alanı) #260'tadır.
--   * gaming ve sports alanları (oyun, platform; dal, saha adı) tanımdadır;
--     değerleri event_field_values'a bu iş kaydının devamında taşınır.
--   * camp ve hike alanları (kamp alanı, su, tuvalet, not; zorluk, mesafe,
--     irtifa) tanımda YOKTUR: değerleri hâlâ event_camp / event_hike'tadır
--     ve #241 (checklist) ile #242 (roll_call) taşırken tanıma ekler.
--   * Bileşenler (route, vehicles, checklist, roll_call, log) bugünkü türe
--     özel araçların karşılığıdır; verileri ilgili bileşen iş kayıtlarında
--     taşınır (#239–#243).
--   * İkon anahtarları Material Symbols adlarıdır; istemcinin sabit ikon
--     haritasıyla (Tayfam-App ADR-0024 §2) hizalanır.
--
-- Geri alma planı: Down dört tabloyu ve trigger fonksiyonunu kaldırır.
-- Katalog yalnızca seed verisi taşır (kullanıcı verisi yok); başka hiçbir
-- tablo bu migration'da kataloğa bağlanmaz, bu yüzden geri alma veri
-- kaybettirmez.

-- +goose Up

CREATE TABLE catalog_meta (
  id              boolean PRIMARY KEY DEFAULT true,
  catalog_version bigint NOT NULL,
  updated_at      timestamptz NOT NULL DEFAULT now(),

  -- Tek satır.
  CONSTRAINT catalog_meta_single CHECK (id),
  CONSTRAINT catalog_meta_version CHECK (catalog_version >= 1)
);
CREATE TRIGGER catalog_meta_updated_at BEFORE UPDATE ON catalog_meta
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE catalog_interests (
  key             text PRIMARY KEY,
  -- Listeleme sırası.
  position        integer NOT NULL,
  -- Dil → metin ({"tr": "Doğa"}); platform sahibinin yazdığı içerik.
  labels          jsonb NOT NULL,
  -- İstemcinin sabit ikon setinden bir anahtar; sunucu görsel taşımaz.
  icon            text NOT NULL,
  status          text NOT NULL DEFAULT 'active',
  -- Bu ilgi alanını seçen tayfada varsayılan açık gelen ana sayfa
  -- bileşenleri (ADR-0027).
  default_widgets text[] NOT NULL DEFAULT '{}',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT catalog_interests_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_interests_position CHECK (position BETWEEN 0 AND 9999),
  CONSTRAINT catalog_interests_labels CHECK (
    jsonb_typeof(labels) = 'object' AND jsonb_typeof(labels -> 'tr') = 'string'),
  CONSTRAINT catalog_interests_icon CHECK (icon ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_interests_status CHECK (status IN ('active', 'retired')),
  CONSTRAINT catalog_interests_default_widgets CHECK (
    cardinality(default_widgets) <= 20 AND array_position(default_widgets, NULL) IS NULL)
);
CREATE TRIGGER catalog_interests_updated_at BEFORE UPDATE ON catalog_interests
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE catalog_event_types (
  key           text PRIMARY KEY,
  -- 1–3 ilgi alanı; ilki ana ilgi alanıdır (ADR-0037 §1). Anahtarların
  -- varlığını aşağıdaki trigger denetler (dizide foreign key olmaz).
  interest_keys text[] NOT NULL,
  -- Listeleme sırası.
  position      integer NOT NULL,
  labels        jsonb NOT NULL,
  icon          text NOT NULL,
  modes         text[] NOT NULL,
  status        text NOT NULL DEFAULT 'active',
  -- Güncel sürüm; aynı (key, version) catalog_event_type_versions'ta vardır.
  version       integer NOT NULL,
  definition    jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT catalog_event_types_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_event_types_interest_keys CHECK (
    cardinality(interest_keys) BETWEEN 1 AND 3
    AND array_position(interest_keys, NULL) IS NULL
    AND (cardinality(interest_keys) < 2 OR interest_keys[1] <> interest_keys[2])
    AND (cardinality(interest_keys) < 3 OR (interest_keys[1] <> interest_keys[3] AND interest_keys[2] <> interest_keys[3]))),
  CONSTRAINT catalog_event_types_position CHECK (position BETWEEN 0 AND 9999),
  CONSTRAINT catalog_event_types_labels CHECK (
    jsonb_typeof(labels) = 'object' AND jsonb_typeof(labels -> 'tr') = 'string'),
  CONSTRAINT catalog_event_types_icon CHECK (icon ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_event_types_modes CHECK (
    cardinality(modes) BETWEEN 1 AND 2
    AND array_position(modes, NULL) IS NULL
    AND modes <@ ARRAY['in_person', 'online']::text[]
    AND (cardinality(modes) < 2 OR modes[1] <> modes[2])),
  CONSTRAINT catalog_event_types_status CHECK (status IN ('active', 'retired')),
  CONSTRAINT catalog_event_types_version CHECK (version >= 1),
  CONSTRAINT catalog_event_types_definition CHECK (
    jsonb_typeof(definition) = 'object' AND octet_length(definition::text) <= 32768)
);
CREATE TRIGGER catalog_event_types_updated_at BEFORE UPDATE ON catalog_event_types
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- İlgi alanı anahtarları katalogda olmalıdır.
-- +goose StatementBegin
CREATE FUNCTION catalog_event_types_interests() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM unnest(NEW.interest_keys) AS k
    WHERE NOT EXISTS (SELECT 1 FROM catalog_interests i WHERE i.key = k)
  ) THEN
    RAISE EXCEPTION 'catalog_event_types: bilinmeyen ilgi alanı'
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_event_types_interests BEFORE INSERT OR UPDATE OF interest_keys ON catalog_event_types
  FOR EACH ROW EXECUTE FUNCTION catalog_event_types_interests();

-- Bir tür sürümünün değişmez kopyası. Etkinlik oluşturulduğu andaki sürümle
-- doğrulanır ve gösterilir (ADR-0025 §2); tanım sonradan değişse de eski
-- etkinliğin anlamı değişmez. Ad, ikon ve mod da sürümle birlikte saklanır:
-- eski etkinlik, türü emekliye ayrılsa bile kendi sürümündeki adıyla görünür.
CREATE TABLE catalog_event_type_versions (
  type_key   text NOT NULL REFERENCES catalog_event_types (key),
  version    integer NOT NULL,
  labels     jsonb NOT NULL,
  icon       text NOT NULL,
  modes      text[] NOT NULL,
  definition jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (type_key, version),
  CONSTRAINT catalog_event_type_versions_version CHECK (version >= 1),
  CONSTRAINT catalog_event_type_versions_labels CHECK (
    jsonb_typeof(labels) = 'object' AND jsonb_typeof(labels -> 'tr') = 'string'),
  CONSTRAINT catalog_event_type_versions_icon CHECK (icon ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_event_type_versions_modes CHECK (
    cardinality(modes) BETWEEN 1 AND 2
    AND array_position(modes, NULL) IS NULL
    AND modes <@ ARRAY['in_person', 'online']::text[]
    AND (cardinality(modes) < 2 OR modes[1] <> modes[2])),
  CONSTRAINT catalog_event_type_versions_definition CHECK (
    jsonb_typeof(definition) = 'object' AND octet_length(definition::text) <= 32768)
);

-- Türün güncel sürümünün kopyası her zaman vardır. İki tablo birbirine
-- bağlı olduğu için kontrol transaction sonuna ertelenir.
ALTER TABLE catalog_event_types
  ADD CONSTRAINT catalog_event_types_current_version_fkey
  FOREIGN KEY (key, version) REFERENCES catalog_event_type_versions (type_key, version)
  DEFERRABLE INITIALLY DEFERRED;

-- core_app yalnızca okur (varsayılan yetki SELECT ve INSERT'tir, 00016).
REVOKE INSERT ON catalog_meta, catalog_interests, catalog_event_types, catalog_event_type_versions FROM core_app;

-- ---------------------------------------------------------------------------
-- Seed: Faz 1'in yedi türü
-- ---------------------------------------------------------------------------

INSERT INTO catalog_meta (catalog_version) VALUES (1);

INSERT INTO catalog_interests (key, position, labels, icon) VALUES
  ('automotive', 10, '{"tr": "Otomobil ve motor"}', 'directions_car'),
  ('nature',     20, '{"tr": "Doğa"}',              'forest'),
  ('water',      30, '{"tr": "Su"}',                'water'),
  ('sports',     40, '{"tr": "Spor"}',              'sports'),
  ('games',      50, '{"tr": "Oyun"}',              'casino'),
  ('general',    900, '{"tr": "Genel"}',            'event');

INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition) VALUES
  ('convoy', ARRAY['automotive'], 10, '{"tr": "Konvoy"}', 'directions_car', ARRAY['in_person', 'online'], 1,
   '{"components": [
      {"kind": "meeting_point"},
      {"kind": "route"},
      {"kind": "vehicles", "params": {"plate": true}}
    ]}'),
  ('hike', ARRAY['nature'], 20, '{"tr": "Doğa yürüyüşü"}', 'hiking', ARRAY['in_person', 'online'], 1,
   '{"components": [
      {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}
    ]}'),
  ('camp', ARRAY['nature'], 30, '{"tr": "Kamp"}', 'camping', ARRAY['in_person', 'online'], 1,
   '{"components": [
      {"kind": "checklist", "key": "gear", "labels": {"tr": "Malzeme listesi"},
       "params": {"label": "gear", "assignment": true, "quantity": true}}
    ]}'),
  ('fishing', ARRAY['water'], 40, '{"tr": "Balıkçılık"}', 'phishing', ARRAY['in_person', 'online'], 1,
   '{"components": [
      {"kind": "meeting_point"},
      {"kind": "log", "key": "catches", "labels": {"tr": "Av günlüğü"},
       "params": {
         "fields": [
           {"key": "species", "kind": "text", "required": true, "max_length": 60, "labels": {"tr": "Tür"}},
           {"key": "length_cm", "kind": "decimal", "required": true, "min": 0.1, "max": 1000, "decimals": 1,
            "labels": {"tr": "Boy (cm)"}}
         ],
         "location": true, "location_default": "self", "max_entries": 100}}
    ]}'),
  ('sports', ARRAY['sports'], 50, '{"tr": "Spor (diğer)"}', 'sports', ARRAY['in_person', 'online'], 1,
   '{"fields": [
      {"key": "sport", "kind": "choice", "required": true,
       "options": ["football", "table_football", "basketball", "volleyball", "tennis", "table_tennis", "other"],
       "option_labels": {
         "football": {"tr": "Futbol"}, "table_football": {"tr": "Langırt"},
         "basketball": {"tr": "Basketbol"}, "volleyball": {"tr": "Voleybol"},
         "tennis": {"tr": "Tenis"}, "table_tennis": {"tr": "Masa tenisi"}, "other": {"tr": "Diğer"}},
       "labels": {"tr": "Dal"}},
      {"key": "venue_name", "kind": "text", "max_length": 120, "labels": {"tr": "Saha adı"}}
    ]}'),
  ('gaming', ARRAY['games'], 60, '{"tr": "Video oyunu"}', 'sports_esports', ARRAY['in_person', 'online'], 1,
   '{"fields": [
      {"key": "game", "kind": "text", "required": true, "max_length": 60, "labels": {"tr": "Oyun"}},
      {"key": "platform", "kind": "choice", "required": true,
       "options": ["pc", "playstation", "xbox", "switch", "mobile", "other"],
       "option_labels": {
         "pc": {"tr": "PC"}, "playstation": {"tr": "PlayStation"}, "xbox": {"tr": "Xbox"},
         "switch": {"tr": "Switch"}, "mobile": {"tr": "Mobil"}, "other": {"tr": "Diğer"}},
       "labels": {"tr": "Platform"}}
    ]}'),
  ('general', ARRAY['general'], 900, '{"tr": "Genel"}', 'event', ARRAY['in_person', 'online'], 1,
   '{}');

INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
SELECT key, version, labels, icon, modes, definition FROM catalog_event_types;

-- +goose Down
ALTER TABLE catalog_event_types DROP CONSTRAINT catalog_event_types_current_version_fkey;
DROP TABLE catalog_event_type_versions;
DROP TABLE catalog_event_types;
DROP FUNCTION catalog_event_types_interests();
DROP TABLE catalog_interests;
DROP TABLE catalog_meta;
