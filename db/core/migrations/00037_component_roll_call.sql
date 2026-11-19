-- Yoklama ve dönmeme alarmı bileşeni (`roll_call`; issue #242; ADR-0025 §4,
-- §9, §10; ADR-0041 madde 3) ve yürüyüş türünün Faz 1 yapısının
-- kaldırılması.
--
--   event_roll_calls          bir etkinliğin bir yoklama bileşeninin planı:
--                             beklenen dönüş anı. Organizatörün yazdığı plan
--                             verisidir; kişi kimliği taşımaz. Planı olmayan
--                             bileşenin satırı yoktur (alarm kurulmaz; Faz 1
--                             expected_return_at NULL ile aynı).
--   event_roll_call_checkins  kimin çıktığı ve döndüğü: istemcinin bildirdiği
--                             çıkış ve dönüş anları, sunucunun aldığı anlar ve
--                             dönmeme alarmının gönderildiği an. Kişiye aittir:
--                             dışa aktarmaya girer, hesap silmede silinir.
--                             Cihaz konumu tutulmaz (ADR-0008).
--
-- Her iki tablo da (event_id, component_key) ile etkinliğe ve tür
-- sürümündeki bileşene bağlıdır; ortak tetikleyici
-- (event_component_guard('roll_call'), 00033) bileşeni olmayan ya da bileşeni
-- kapalı etkinliğe yazmayı reddeder. Dönüş (returned_at) ve alarm
-- (alert_sent_at) güncellemeleri tetikleyiciden geçmez: güvenlik sinyali
-- bileşenin seçimi yüzünden kaybolmaz.
--
-- Alarmın açık olup olmadığı ve dönüş toleransı tür tanımındaki bileşen
-- parametreleridir (`alarm`, `return_grace_minutes`; yoksa 30 dakika). Faz 1'de
-- tolerans etkinlik başınaydı; artık tür sürümünden okunur.
--
-- Yürüyüş türü tanımı (catalog_event_types ve catalog_event_type_versions,
-- hike v1) yerinde güncellenir (ADR-0041; issue #242 yorumu, #237): Faz 1
-- yürüyüş ayrıntıları alan olur — difficulty (tam sayı 1–5),
-- planned_distance (ondalık, km, 0,01–1000, iki basamak), planned_elevation
-- (tam sayı, m, 0–9000); hepsi isteğe bağlı, sınırlar Faz 1 CHECK'leriyle
-- aynı. Katalog değiştiği için catalog_meta.catalog_version bir artar.
--
-- VERİ TAŞIYAN VE TABLO SİLEN MIGRATION (hassas alan; ADR-0041 madde 3):
--   * `event_hike` satırlarının dolu değerleri `event_field_values`'a
--     kopyalanır (difficulty, planned_distance, planned_elevation; NULL olan
--     değer yazılmaz). Alan tetikleyicisi (event_field_values_guard, 00031)
--     tanım güncellendikten sonra çalışır: alan etkinliğin tür sürümünde
--     yoksa migration hata verir ve hiçbir şey değişmez.
--   * `event_hike.expected_return_at` dolu olan etkinliklerin planı
--     `event_roll_calls`'a (component_key = 'roll_call') kopyalanır.
--   * `event_hike_checkins` satırları `event_roll_call_checkins`'e
--     (component_key = 'roll_call') bütün kolonlarıyla, alarm durumu dahil,
--     kopyalanır: gönderilmiş alarm yeniden gönderilmez, bekleyen alarm
--     bekler.
--   * İki kopya da ortak bileşen tetikleyicisinden geçer: yürüyüş türünün
--     etkinliğin bağlı olduğu sürümünde `roll_call` yoksa migration hata
--     verir (sessiz veri kaybı olmaz). Hike v1 tanımında vardır (00030).
--   * `event_hike.return_grace_minutes`: 30 dışındaki etkinlik başına
--     tolerans değerleri taşınmaz (tolerans artık tür tanımındadır, hike v1:
--     30 dakika).
--   * Ardından `event_hike_checkins` ve `event_hike` kaldırılır.
--
-- Geri alma planı: Down iki tabloyu Faz 1 şemasıyla (dizinler, tetikleyici ve
-- core_app yetkileriyle) geri kurar, her yürüyüş etkinliğine detay satırını
-- alan değerlerinden ve `roll_call` planından yazar (tolerans 30), yürüyüş
-- etkinliklerinin `roll_call` yoklama satırlarını geri kopyalar, yürüyüş
-- etkinliklerinin bu üç alan değerini siler, alanları hike v1 tanımından
-- çıkarır (catalog_version yine bir artar) ve bileşen tablolarını kaldırır.
-- Yürüyüş dışındaki türlerin yoklamaları Down ile kaybolur: Faz 1 şemasında
-- yerleri yoktur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_roll_calls (
  event_id           uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'roll_call').
  component_key      text NOT NULL,
  -- Beklenen dönüş; alarm bu an + tolerans geçince kurulur. Etkinliğin
  -- başlangıcından sonra olmalıdır (uygulama denetler).
  expected_return_at timestamptz NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  -- Planın son yazıldığı an; uygulama yazar (isteğin zamanı).
  updated_at         timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_roll_calls_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$')
);
CREATE TRIGGER event_roll_calls_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_roll_calls
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('roll_call');

CREATE TABLE event_roll_call_checkins (
  event_id             uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'roll_call').
  component_key        text NOT NULL,
  user_id              uuid NOT NULL,
  -- İstemcinin bildirdiği çıkış anı ve sunucunun aldığı an.
  started_at           timestamptz NOT NULL,
  started_received_at  timestamptz NOT NULL,
  -- İstemcinin bildirdiği dönüş anı ve sunucunun aldığı an; dışarıdayken NULL.
  returned_at          timestamptz,
  returned_received_at timestamptz,
  -- Dönmeme alarmının kurulduğu an (jobs: hike-alert); bir kez.
  alert_sent_at        timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key, user_id),
  CONSTRAINT event_roll_call_checkins_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_roll_call_checkins_returned CHECK ((returned_at IS NULL) = (returned_received_at IS NULL)),
  CONSTRAINT event_roll_call_checkins_order CHECK (returned_at IS NULL OR returned_at >= started_at),
  -- İstemci zamanı gelecekte olamaz, 48 saatten eski olamaz; uygulama
  -- birkaç dakikalık saat kaymasına izin verir (Faz 1 ile aynı).
  CONSTRAINT event_roll_call_checkins_started_at CHECK (started_at BETWEEN started_received_at - interval '48 hours' AND started_received_at + interval '5 minutes'),
  CONSTRAINT event_roll_call_checkins_returned_at CHECK (returned_at IS NULL OR returned_at BETWEEN returned_received_at - interval '48 hours' AND returned_received_at + interval '5 minutes')
);
-- Hesap silme ve dışa aktarma.
CREATE INDEX event_roll_call_checkins_user_id_idx ON event_roll_call_checkins (user_id, event_id);
-- Dönmeme alarmı işi.
CREATE INDEX event_roll_call_checkins_alert_idx ON event_roll_call_checkins (event_id, component_key)
  WHERE returned_at IS NULL AND alert_sent_at IS NULL;
CREATE TRIGGER event_roll_call_checkins_updated_at BEFORE UPDATE ON event_roll_call_checkins
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER event_roll_call_checkins_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_roll_call_checkins
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('roll_call');

-- Bir yoklama bileşeni tanımının alarm toleransı (dakika): alarm açıksa
-- return_grace_minutes (yoksa 30), alarmsız ya da tanımsız bileşende NULL.
-- Dönmeme alarmı işi (LockOverdueHikers) bunu
-- event_component_definition(event_id, component_key) ile çağırır.
-- +goose StatementBegin
CREATE FUNCTION roll_call_alarm_minutes(p_def jsonb) RETURNS integer
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE WHEN p_def ->> 'kind' = 'roll_call' AND p_def -> 'params' -> 'alarm' = 'true'::jsonb
    THEN COALESCE((p_def -> 'params' ->> 'return_grace_minutes')::integer, 30)
  END
$$;
-- +goose StatementEnd

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016). Plan
-- değişir (ON CONFLICT) ve kaldırılır. Yoklama satırına yalnızca dönüş ve
-- alarm yazılır; çıkış anı değişmez, satır başka etkinliğe, bileşene ya da
-- kişiye taşınamaz. Satır hesap silmede silinir. SELECT ve INSERT burada da
-- açıkça verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_roll_calls TO core_app;
GRANT UPDATE (expected_return_at, updated_at) ON event_roll_calls TO core_app;
GRANT SELECT, INSERT, DELETE ON event_roll_call_checkins TO core_app;
GRANT UPDATE (returned_at, returned_received_at, alert_sent_at, updated_at) ON event_roll_call_checkins TO core_app;

-- Hike v1 tanımına Faz 1 yürüyüş ayrıntıları alan olarak eklenir.
UPDATE catalog_event_types SET definition = definition || '{"fields": [
    {"key": "difficulty", "kind": "integer", "min": 1, "max": 5, "labels": {"tr": "Zorluk"}},
    {"key": "planned_distance", "kind": "decimal", "min": 0.01, "max": 1000, "decimals": 2, "unit": "km",
     "labels": {"tr": "Planlanan mesafe"}},
    {"key": "planned_elevation", "kind": "integer", "min": 0, "max": 9000, "unit": "m",
     "labels": {"tr": "Planlanan irtifa"}}
  ]}'::jsonb
WHERE key = 'hike' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'hike' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- Faz 1 verisi: yürüyüş ayrıntıları alan değerlerine.
INSERT INTO event_field_values (event_id, field_key, value)
SELECT h.event_id, 'difficulty', to_jsonb(h.difficulty) FROM event_hike AS h WHERE h.difficulty IS NOT NULL
UNION ALL
SELECT h.event_id, 'planned_distance', to_jsonb(h.distance_km::float8) FROM event_hike AS h WHERE h.distance_km IS NOT NULL
UNION ALL
SELECT h.event_id, 'planned_elevation', to_jsonb(h.elevation_m) FROM event_hike AS h WHERE h.elevation_m IS NOT NULL;

-- Faz 1 verisi: beklenen dönüş yoklama planına.
INSERT INTO event_roll_calls (event_id, component_key, expected_return_at, created_at, updated_at)
SELECT h.event_id, 'roll_call', h.expected_return_at, e.created_at, e.updated_at
FROM event_hike AS h
JOIN events AS e ON e.id = h.event_id
WHERE h.expected_return_at IS NOT NULL;

-- Faz 1 verisi: yürüyüş yoklamaları, alarm durumuyla.
INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at,
  returned_at, returned_received_at, alert_sent_at, created_at, updated_at)
SELECT c.event_id, 'roll_call', c.user_id, c.started_at, c.started_received_at,
  c.returned_at, c.returned_received_at, c.alert_sent_at, c.created_at, c.updated_at
FROM event_hike_checkins AS c;

-- Eski tablolar; dizinleri, tetikleyicileri ve yetkileri tablolarla birlikte
-- kalkar.
DROP TABLE event_hike_checkins;
DROP TABLE event_hike;

-- +goose Down

CREATE TABLE event_hike (
  event_id             uuid PRIMARY KEY,
  type                 text NOT NULL DEFAULT 'hike',
  difficulty           integer,
  distance_km          numeric(6, 2),
  elevation_m          integer,
  expected_return_at   timestamptz,
  return_grace_minutes integer NOT NULL DEFAULT 30,

  CONSTRAINT event_hike_type CHECK (type = 'hike'),
  CONSTRAINT event_hike_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_hike_difficulty CHECK (difficulty IS NULL OR difficulty BETWEEN 1 AND 5),
  CONSTRAINT event_hike_distance_km CHECK (distance_km IS NULL OR (distance_km > 0 AND distance_km <= 1000)),
  CONSTRAINT event_hike_elevation_m CHECK (elevation_m IS NULL OR elevation_m BETWEEN 0 AND 9000),
  CONSTRAINT event_hike_return_grace_minutes CHECK (return_grace_minutes BETWEEN 0 AND 1440)
);

CREATE TABLE event_hike_checkins (
  event_id             uuid NOT NULL REFERENCES event_hike (event_id) ON DELETE CASCADE,
  user_id              uuid NOT NULL,
  started_at           timestamptz NOT NULL,
  started_received_at  timestamptz NOT NULL,
  returned_at          timestamptz,
  returned_received_at timestamptz,
  alert_sent_at        timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, user_id),
  CONSTRAINT event_hike_checkins_returned CHECK ((returned_at IS NULL) = (returned_received_at IS NULL)),
  CONSTRAINT event_hike_checkins_order CHECK (returned_at IS NULL OR returned_at >= started_at),
  CONSTRAINT event_hike_checkins_started_at CHECK (started_at BETWEEN started_received_at - interval '48 hours' AND started_received_at + interval '5 minutes'),
  CONSTRAINT event_hike_checkins_returned_at CHECK (returned_at IS NULL OR returned_at BETWEEN returned_received_at - interval '48 hours' AND returned_received_at + interval '5 minutes')
);
CREATE INDEX event_hike_checkins_user_id_idx ON event_hike_checkins (user_id);
CREATE INDEX event_hike_checkins_out_idx ON event_hike_checkins (event_id) WHERE returned_at IS NULL;
CREATE INDEX event_hike_checkins_alert_idx ON event_hike_checkins (event_id) WHERE returned_at IS NULL AND alert_sent_at IS NULL;
CREATE TRIGGER event_hike_checkins_updated_at BEFORE UPDATE ON event_hike_checkins FOR EACH ROW EXECUTE FUNCTION set_updated_at();

GRANT SELECT, INSERT, UPDATE ON event_hike TO core_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON event_hike_checkins TO core_app;

INSERT INTO event_hike (event_id, difficulty, distance_km, elevation_m, expected_return_at)
SELECT e.id, (d.value #>> '{}')::integer, (k.value #>> '{}')::numeric(6, 2), (m.value #>> '{}')::integer, r.expected_return_at
FROM events AS e
LEFT JOIN event_field_values AS d ON d.event_id = e.id AND d.field_key = 'difficulty'
LEFT JOIN event_field_values AS k ON k.event_id = e.id AND k.field_key = 'planned_distance'
LEFT JOIN event_field_values AS m ON m.event_id = e.id AND m.field_key = 'planned_elevation'
LEFT JOIN event_roll_calls AS r ON r.event_id = e.id AND r.component_key = 'roll_call'
WHERE e.type = 'hike';

INSERT INTO event_hike_checkins (event_id, user_id, started_at, started_received_at,
  returned_at, returned_received_at, alert_sent_at, created_at, updated_at)
SELECT c.event_id, c.user_id, c.started_at, c.started_received_at,
  c.returned_at, c.returned_received_at, c.alert_sent_at, c.created_at, c.updated_at
FROM event_roll_call_checkins AS c
JOIN events AS e ON e.id = c.event_id
WHERE c.component_key = 'roll_call' AND e.type = 'hike';

DELETE FROM event_field_values AS f
USING events AS e
WHERE e.id = f.event_id AND e.type = 'hike'
  AND f.field_key IN ('difficulty', 'planned_distance', 'planned_elevation');

UPDATE catalog_event_types SET definition = definition - 'fields'
WHERE key = 'hike' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'hike' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

DROP TABLE event_roll_call_checkins;
DROP TABLE event_roll_calls;
DROP FUNCTION roll_call_alarm_minutes(jsonb);
