-- Etkinlik tür sürümüne bağlanır; türe özel alanların değerleri
-- event_field_values'tadır (issue #237; ADR-0025 §2, §3, §10; ADR-0041).
--
--   events.type_version   etkinliğin oluşturulduğu tür sürümü. Etkinlik o
--                         sürümün tanımıyla doğrulanır ve gösterilir; tanım
--                         sonradan değişse de anlamı değişmez.
--   events.type           artık katalog anahtarıdır: CHECK listesi kalkar,
--                         yerine (type, type_version) →
--                         catalog_event_type_versions foreign key'i gelir.
--                         Katalogda olmayan tür yazılamaz.
--   user_activity_records.event_type
--                         CHECK listesi kalkar; katalog anahtarına bağlanır.
--   event_field_values    (event_id, field_key, value jsonb): tür tanımındaki
--                         "fields" alanlarının değerleri. Değer Go'da tür
--                         sürümüne göre doğrulanır (definition.CleanValues);
--                         buradaki trigger ikinci savunma hattıdır: alan,
--                         etkinliğin tür sürümünün tanımında yoksa yazılamaz.
--
-- Veri taşıma (ADR-0025 §10 madde 2, ADR-0041 §3): event_gaming (oyun,
-- platform) ve event_sports (dal, saha adı) satırları event_field_values'a
-- kopyalanır ve iki tablo KALDIRILIR. Değerler aynen korunur; seçenek
-- anahtarları katalogdaki gaming / sports v1 tanımındakilerle aynıdır.
-- Diğer türe özel tablolar (event_convoy, event_camp, event_hike,
-- event_fishing ve alt tabloları) bu migration'da değişmez; bileşen iş
-- kayıtlarında taşınır (#239–#243).
--
-- type_version varsayılanı 1'dir: mevcut satırlar (yedi Faz 1 türü, hepsi
-- sürüm 1) bununla dolar. Uygulama her yeni etkinlikte sürümü açıkça yazar.
--
-- Geri alma planı: Down iki tabloyu 00004'teki tanımlarıyla yeniden açar,
-- değerleri event_field_values'tan geri kopyalar, sonra yeni tabloyu,
-- foreign key'leri ve kolonu kaldırıp CHECK listelerini geri koyar. Geri
-- alma, yedi Faz 1 türü dışında bir türle açılmış etkinlik ya da katılım
-- kaydı varsa CHECK'te durur (o kayıtlar önce elle ele alınmalıdır); gaming
-- ve sports dışındaki türlerin alan değerleri geri almada kaybolur. Prod'daki
-- veri yalnızca demo ve platform sahibinin verisidir (ADR-0041 §3); yine de
-- uygulamadan önce veritabanı yedeği alınır (docs/runbooks).

-- +goose Up

ALTER TABLE events ADD COLUMN type_version integer NOT NULL DEFAULT 1;
ALTER TABLE events DROP CONSTRAINT events_type;
ALTER TABLE events ADD CONSTRAINT events_type_version_fkey
  FOREIGN KEY (type, type_version) REFERENCES catalog_event_type_versions (type_key, version);

ALTER TABLE user_activity_records DROP CONSTRAINT user_activity_records_event_type;
ALTER TABLE user_activity_records ADD CONSTRAINT user_activity_records_event_type_fkey
  FOREIGN KEY (event_type) REFERENCES catalog_event_types (key);

CREATE TABLE event_field_values (
  event_id   uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki alanın anahtarı.
  field_key  text NOT NULL,
  -- Alan türüne göre: metin, sayı, true/false ya da metin dizisi
  -- (multi_choice). Serbest metin kullanıcı içeriğidir; log'a ve olaya
  -- yazılmaz.
  value      jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, field_key),
  CONSTRAINT event_field_values_field_key CHECK (field_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_field_values_value CHECK (
    jsonb_typeof(value) IN ('string', 'number', 'boolean', 'array')
    AND octet_length(value::text) <= 8192)
);
CREATE TRIGGER event_field_values_updated_at BEFORE UPDATE ON event_field_values
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Alan, etkinliğin tür sürümünün tanımında olmalıdır.
-- +goose StatementBegin
CREATE FUNCTION event_field_values_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM events e
    JOIN catalog_event_type_versions v ON v.type_key = e.type AND v.version = e.type_version
    CROSS JOIN LATERAL jsonb_array_elements(
      CASE WHEN jsonb_typeof(v.definition -> 'fields') = 'array' THEN v.definition -> 'fields' ELSE '[]'::jsonb END
    ) AS f
    WHERE e.id = NEW.event_id AND f ->> 'key' = NEW.field_key
  ) THEN
    RAISE EXCEPTION 'event_field_values: alan etkinliğin tür sürümünde yok'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_field_values_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_field_values_guard BEFORE INSERT OR UPDATE OF event_id, field_key ON event_field_values
  FOR EACH ROW EXECUTE FUNCTION event_field_values_guard();

-- core_app (ADR-0015): değeri değiştirir (ON CONFLICT) ve etkinliğin alan
-- değerlerini siler. event_id ve field_key değiştirilemez. Varsayılan yetki
-- (00016) SELECT ve INSERT'tir.
GRANT UPDATE (value, updated_at), DELETE ON event_field_values TO core_app;

-- gaming ve sports alanlarının taşınması.
INSERT INTO event_field_values (event_id, field_key, value)
SELECT event_id, 'game', to_jsonb(game) FROM event_gaming
UNION ALL
SELECT event_id, 'platform', to_jsonb(platform) FROM event_gaming
UNION ALL
SELECT event_id, 'sport', to_jsonb(sport) FROM event_sports
UNION ALL
SELECT event_id, 'venue_name', to_jsonb(venue_name) FROM event_sports WHERE venue_name IS NOT NULL;

DROP TABLE event_gaming;
DROP TABLE event_sports;

-- +goose Down

CREATE TABLE event_gaming (
  event_id uuid PRIMARY KEY,
  type     text NOT NULL DEFAULT 'gaming',
  game     text NOT NULL,
  platform text NOT NULL,

  CONSTRAINT event_gaming_type CHECK (type = 'gaming'),
  CONSTRAINT event_gaming_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_gaming_game_len CHECK (char_length(game) BETWEEN 1 AND 60),
  CONSTRAINT event_gaming_platform CHECK (platform IN ('pc', 'playstation', 'xbox', 'switch', 'mobile', 'other'))
);

CREATE TABLE event_sports (
  event_id   uuid PRIMARY KEY,
  type       text NOT NULL DEFAULT 'sports',
  sport      text NOT NULL,
  venue_name text,

  CONSTRAINT event_sports_type CHECK (type = 'sports'),
  CONSTRAINT event_sports_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_sports_sport CHECK (sport IN ('football', 'table_football', 'basketball', 'volleyball', 'tennis', 'table_tennis', 'other')),
  CONSTRAINT event_sports_venue_name_len CHECK (venue_name IS NULL OR char_length(venue_name) BETWEEN 1 AND 120)
);
GRANT UPDATE ON event_gaming, event_sports TO core_app;

INSERT INTO event_gaming (event_id, game, platform)
SELECT e.id, g.value #>> '{}', p.value #>> '{}'
FROM events e
JOIN event_field_values g ON g.event_id = e.id AND g.field_key = 'game'
JOIN event_field_values p ON p.event_id = e.id AND p.field_key = 'platform'
WHERE e.type = 'gaming';

INSERT INTO event_sports (event_id, sport, venue_name)
SELECT e.id, s.value #>> '{}', v.value #>> '{}'
FROM events e
JOIN event_field_values s ON s.event_id = e.id AND s.field_key = 'sport'
LEFT JOIN event_field_values v ON v.event_id = e.id AND v.field_key = 'venue_name'
WHERE e.type = 'sports';

DROP TABLE event_field_values;
DROP FUNCTION event_field_values_guard();

ALTER TABLE user_activity_records DROP CONSTRAINT user_activity_records_event_type_fkey;
ALTER TABLE user_activity_records ADD CONSTRAINT user_activity_records_event_type
  CHECK (event_type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general'));

ALTER TABLE events DROP CONSTRAINT events_type_version_fkey;
ALTER TABLE events ADD CONSTRAINT events_type
  CHECK (type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general'));
ALTER TABLE events DROP COLUMN type_version;
