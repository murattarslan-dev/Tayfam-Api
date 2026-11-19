-- Günlük bileşeni (`log`; issue #243; ADR-0025 §4, §5, §10; ADR-0037 §3;
-- ADR-0041 madde 3) ve balıkçılık türünün Faz 1 yapısının kaldırılması.
--
--   event_log_entries  bir etkinliğin bir günlük bileşenindeki kayıtlar (av,
--                      kitap, kuş, zirve …). Kaydın alanları tür tanımındaki
--                      bileşenin `fields` parametresindendir (ADR-0025 §3
--                      alan türleri); değerler `field_values`'ta alan
--                      anahtarına göre, uygulamanın doğruladığı saklama
--                      biçimiyle (definition.CleanValues) durur: metin
--                      kırpılmış dize, sayı JSON sayısı. İstatistik (#266)
--                      ve tayfa sözlüğü (#244) bu değerleri okur.
--                      Konum isteğe bağlıdır (bileşenin `location`
--                      parametresi) ve kişinin bilerek paylaştığı içeriktir
--                      (ADR-0008); kimin göreceği kayıt başına
--                      `location_visibility`'dir (self / participants /
--                      tayfa; varsayılanı bileşenin `location_default`
--                      parametresi). Fotoğraf isteğe bağlıdır (ADR-0020,
--                      amaç `log_photo`). Kayıt (event_id, component_key)
--                      ile etkinliğe ve tür sürümündeki bileşene bağlıdır;
--                      ortak tetikleyici (event_component_guard('log'),
--                      00033) bileşeni olmayan ya da kapalı etkinliğe
--                      yazmayı reddeder. İçerik tablosudur: soft delete,
--                      kalıcı silme 30 gün sonra arka plan işiyle.
--
-- Balıkçılık türü tanımı (catalog_event_types ve
-- catalog_event_type_versions, fishing v1) yerinde güncellenir (ADR-0041:
-- geriye uyumluluk yok; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri
-- (2026-11-18)", Su: günlük `catches` — tür [sözlük], adet, ağırlık; mera
-- gizliliği varsayılan "yalnızca ben"; stat sum:weight, distinct:species):
-- `species` alanı `fish_species` sözlüğüne bağlanır, Faz 1'in `length_cm`
-- alanı kalır ama isteğe bağlı olur, `count` (adet) ve `weight` (kg)
-- eklenir, `stat` verilir. Katalog değiştiği için
-- catalog_meta.catalog_version bir artar (istemci önbelleği).
--
-- Dosya ve şikâyet adları genelleşir: yükleme amacı `catch_photo` →
-- `log_photo`, bağlama türü `fishing_catch` → `log_entry`, şikâyet hedefi
-- `fishing_catch` → `log_entry`. Kimlikler değişmez (kayıt av kaydının
-- kimliğini alır), bu yüzden mevcut bağlamalar ve şikâyetler yerinde
-- güncellenir.
--
-- VERİ TAŞIYAN VE TABLO SİLEN MIGRATION (hassas alan; ADR-0041 madde 3):
--   * `fishing_catches` satırları (silinmişler dahil) `event_log_entries`'e
--     kopyalanır: aynı id, component_key = 'catches', field_values =
--     {species, length_cm}, konum, fotoğraf, tekrar gönderim kimliği,
--     silinme bilgisi ve oluşturma zamanı. Konum görünürlüğü: `self` →
--     `self`; `tayfa` → etkinliğin mera görünürlüğü `participants` ise
--     `participants`, değilse `tayfa` (Faz 1'de `tayfa` "merayı görebilen
--     herkes" demekti); konumsuz kayıtta NULL. Kopya ortak bileşen
--     tetikleyicisinden geçer: etkinliğin tür sürümünde `catches` günlüğü
--     yoksa migration hata verir ve hiçbir şey değişmez (fishing v1'de
--     vardır, 00030). Fotoğraf yalnızca dosya hâlâ hazırsa kopyalanır.
--   * `event_fishing.spot_visibility` kalkar (ADR-0025 §4: mera kuralı
--     günlüğün konum gizliliğine genelleşir). Buluşma noktası artık her
--     etkinlikte olduğu gibi etkinliği görebilen herkese döner: mera
--     görünürlüğü `participants` olan balıkçılık etkinliklerinin buluşma
--     noktası katılmayan üyelere de görünür olur.
--   * Ardından `fishing_catches` ve `event_fishing` kaldırılır.
--
-- Geri alma planı: Down iki tabloyu Faz 1 şemasıyla (fotoğraf kolonu,
-- dizinler, tetikleyiciler ve core_app yetkileriyle) geri kurar, her
-- balıkçılık etkinliğine detay satırı yazar (`participants` görünürlüklü bir
-- kaydı varsa `participants`, yoksa `tayfa`), balıkçılık etkinliklerinin
-- `catches` günlüğündeki türü ve boyu olan kayıtlarını geri kopyalar
-- (`self` ya da konumsuz → `self`, diğerleri → `tayfa`), dosya ve şikâyet
-- adlarını geri çevirir, tanımı 00030'daki haline döndürür
-- (catalog_version yine bir artar) ve bileşen tablosunu kaldırır. Geri
-- kopyalanamayan kayıtlar (balıkçılık dışındaki türlerin günlükleri, boyu
-- olmayan av kayıtları) Down ile silinir; fotoğrafları silme kuyruğuna
-- girer. Adet ve ağırlık değerleri kaybolur. Uygulama kodu da birlikte
-- geri alınmalıdır.

-- +goose Up

CREATE TABLE event_log_entries (
  id                  uuid PRIMARY KEY,
  event_id            uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (günlükte her zaman verilir:
  -- catches, sightings …).
  component_key       text NOT NULL,
  -- Kaydı giren kişi. Hesap silinince kayıtları silinir.
  user_id             uuid NOT NULL,
  -- Alan anahtarı → değer; uygulama tanıma göre doğrular.
  field_values        jsonb NOT NULL DEFAULT '{}',
  lat                 double precision,
  lng                 double precision,
  -- Konumu kim görür; konumsuz kayıtta NULL.
  location_visibility text,
  photo_upload_id     uuid REFERENCES uploads (id) ON DELETE SET NULL,
  client_request_id   uuid NOT NULL,
  deleted_at          timestamptz,
  deleted_by          uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_log_entries_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_log_entries_values CHECK (
    jsonb_typeof(field_values) = 'object' AND octet_length(field_values::text) <= 16384),
  CONSTRAINT event_log_entries_lat CHECK (lat IS NULL OR lat BETWEEN -90 AND 90),
  CONSTRAINT event_log_entries_lng CHECK (lng IS NULL OR lng BETWEEN -180 AND 180),
  CONSTRAINT event_log_entries_location CHECK ((lat IS NULL) = (lng IS NULL)),
  CONSTRAINT event_log_entries_location_visibility CHECK (
    (lat IS NULL) = (location_visibility IS NULL)
    AND (location_visibility IS NULL OR location_visibility IN ('self', 'participants', 'tayfa'))),
  CONSTRAINT event_log_entries_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT event_log_entries_client_request_key UNIQUE (user_id, client_request_id)
);
CREATE INDEX event_log_entries_event_idx ON event_log_entries (event_id, component_key, created_at, id)
  WHERE deleted_at IS NULL;
CREATE INDEX event_log_entries_deleted_at_idx ON event_log_entries (deleted_at) WHERE deleted_at IS NOT NULL;
-- Kayıt sayısı sınırı, hesap silme ve dışa aktarma.
CREATE INDEX event_log_entries_user_idx ON event_log_entries (user_id, event_id, component_key);
CREATE INDEX event_log_entries_deleted_by_idx ON event_log_entries (deleted_by) WHERE deleted_by IS NOT NULL;
CREATE UNIQUE INDEX event_log_entries_photo_upload_id_key ON event_log_entries (photo_upload_id)
  WHERE photo_upload_id IS NOT NULL;
CREATE TRIGGER event_log_entries_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_log_entries
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('log');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): kayıt
-- soft delete ile silinir (deleted_at, deleted_by; hesap silmede silen kişi
-- boşaltılır), 30 gün sonra arka plan işi ve hesap silme kalıcı siler.
-- Kayıt başka etkinliğe, bileşene ya da kişiye taşınamaz; değerleri,
-- konumu ve fotoğrafı değişmez. SELECT ve INSERT burada da açıkça verilir.
GRANT SELECT, INSERT, DELETE ON event_log_entries TO core_app;
GRANT UPDATE (deleted_at, deleted_by) ON event_log_entries TO core_app;

-- Dosya ve şikâyet adları: önce kısıtlar kalkar, değerler değişir, kısıtlar
-- yeni adlarla geri gelir (00026'daki tanımlar, yalnızca bu adlar farklı).
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
UPDATE uploads SET purpose = 'log_photo' WHERE purpose = 'catch_photo';
UPDATE uploads SET attached_type = 'log_entry' WHERE attached_type = 'fishing_catch';
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence'));
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'log_photo' AND attached_type = 'log_entry')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo')
    OR (purpose = 'chat_photo' AND attached_type = 'channel_message')
    OR (purpose = 'dm_attachment' AND attached_type = 'dm_message')
    OR (purpose = 'report_evidence' AND attached_type = 'report'))
);

ALTER TABLE reports DROP CONSTRAINT reports_target_type;
UPDATE reports SET target_type = 'log_entry' WHERE target_type = 'fishing_catch';
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message'));

-- Kaydın fotoğrafı kayda bağlı, hazır ve doğru amaçlı olmalıdır; kayıt
-- kalıcı silinince (cascade dahil) dosya silme kuyruğuna girer (00017).
CREATE TRIGGER event_log_entries_photo_check BEFORE INSERT OR UPDATE OF photo_upload_id ON event_log_entries
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo_upload_id', 'log_photo', 'log_entry');
CREATE TRIGGER event_log_entries_photo_release AFTER UPDATE OF photo_upload_id OR DELETE ON event_log_entries
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo_upload_id');

-- Balıkçılık v1 tanımının `catches` günlüğü Notion kataloğuna göre
-- güncellenir.
UPDATE catalog_event_types SET definition = '{"components": [
    {"kind": "meeting_point"},
    {"kind": "log", "key": "catches", "labels": {"tr": "Av günlüğü"},
     "params": {
       "fields": [
         {"key": "species", "kind": "text", "required": true, "max_length": 60,
          "vocabulary": {"namespace": "fish_species"}, "labels": {"tr": "Tür"}},
         {"key": "count", "kind": "integer", "min": 1, "max": 1000, "unit": "piece", "labels": {"tr": "Adet"}},
         {"key": "weight", "kind": "decimal", "min": 0.01, "max": 1000, "decimals": 2, "unit": "kg",
          "labels": {"tr": "Ağırlık"}},
         {"key": "length_cm", "kind": "decimal", "min": 0.1, "max": 1000, "decimals": 1,
          "labels": {"tr": "Boy (cm)"}}
       ],
       "location": true, "location_default": "self", "max_entries": 100,
       "stat": ["distinct:species", "sum:weight"]}}
  ]}'::jsonb
WHERE key = 'fishing' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'fishing' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- Faz 1 verisi: av kayıtları `catches` günlüğüne.
INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, lat, lng, location_visibility,
  photo_upload_id, client_request_id, deleted_at, deleted_by, created_at)
SELECT c.id, c.event_id, 'catches', c.user_id,
  jsonb_build_object('species', c.species, 'length_cm', c.length_cm::float8),
  c.lat, c.lng,
  CASE
    WHEN c.lat IS NULL THEN NULL
    WHEN c.visibility = 'self' THEN 'self'
    WHEN f.spot_visibility = 'participants' THEN 'participants'
    ELSE 'tayfa'
  END,
  CASE WHEN EXISTS (
    SELECT 1 FROM uploads AS u
    WHERE u.id = c.photo_upload_id AND u.status = 'ready' AND u.purpose = 'log_photo'
      AND u.attached_type = 'log_entry' AND u.attached_id = c.id
  ) THEN c.photo_upload_id END,
  c.client_request_id, c.deleted_at, c.deleted_by, c.created_at
FROM fishing_catches AS c
JOIN event_fishing AS f ON f.event_id = c.event_id;

-- Eski tablolar; dizinleri, tetikleyicileri ve yetkileri tablolarla birlikte
-- kalkar (DROP satır tetikleyicisi çalıştırmaz: fotoğraflar kayıtlarda
-- kalır).
DROP TABLE fishing_catches;
DROP TABLE event_fishing;

-- +goose Down

CREATE TABLE event_fishing (
  event_id        uuid PRIMARY KEY,
  type            text NOT NULL DEFAULT 'fishing',
  spot_visibility text NOT NULL DEFAULT 'tayfa',

  CONSTRAINT event_fishing_type CHECK (type = 'fishing'),
  CONSTRAINT event_fishing_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_fishing_spot_visibility CHECK (spot_visibility IN ('tayfa', 'participants'))
);

CREATE TABLE fishing_catches (
  id                uuid PRIMARY KEY,
  event_id          uuid NOT NULL REFERENCES event_fishing (event_id) ON DELETE CASCADE,
  user_id           uuid NOT NULL,
  species           text NOT NULL,
  length_cm         numeric(5, 1) NOT NULL,
  lat               double precision,
  lng               double precision,
  visibility        text NOT NULL DEFAULT 'self',
  client_request_id uuid NOT NULL,
  deleted_at        timestamptz,
  deleted_by        uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  photo_upload_id   uuid REFERENCES uploads (id) ON DELETE SET NULL,

  CONSTRAINT fishing_catches_species_len CHECK (char_length(species) BETWEEN 1 AND 60),
  CONSTRAINT fishing_catches_length_cm CHECK (length_cm > 0 AND length_cm <= 1000),
  CONSTRAINT fishing_catches_lat CHECK (lat IS NULL OR lat BETWEEN -90 AND 90),
  CONSTRAINT fishing_catches_lng CHECK (lng IS NULL OR lng BETWEEN -180 AND 180),
  CONSTRAINT fishing_catches_location CHECK ((lat IS NULL) = (lng IS NULL)),
  CONSTRAINT fishing_catches_visibility CHECK (visibility IN ('self', 'tayfa')),
  CONSTRAINT fishing_catches_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT fishing_catches_client_request_key UNIQUE (user_id, client_request_id)
);
CREATE INDEX fishing_catches_event_id_idx ON fishing_catches (event_id, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX fishing_catches_deleted_at_idx ON fishing_catches (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE UNIQUE INDEX fishing_catches_photo_upload_id_key ON fishing_catches (photo_upload_id) WHERE photo_upload_id IS NOT NULL;
CREATE TRIGGER fishing_catches_photo_check BEFORE INSERT OR UPDATE OF photo_upload_id ON fishing_catches
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo_upload_id', 'catch_photo', 'fishing_catch');
CREATE TRIGGER fishing_catches_photo_release AFTER UPDATE OF photo_upload_id OR DELETE ON fishing_catches
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo_upload_id');

GRANT SELECT, INSERT, UPDATE ON event_fishing TO core_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON fishing_catches TO core_app;

-- Faz 1 şemasına sığmayan kayıtlar silinir; fotoğrafları silme kuyruğuna
-- girer (event_log_entries_photo_release).
DELETE FROM event_log_entries AS l
WHERE NOT EXISTS (SELECT 1 FROM events AS e WHERE e.id = l.event_id AND e.type = 'fishing')
  OR l.component_key <> 'catches'
  OR NOT COALESCE(CASE WHEN jsonb_typeof(l.field_values -> 'species') = 'string'
    THEN char_length(l.field_values ->> 'species') BETWEEN 1 AND 60 END, false)
  OR NOT COALESCE(CASE WHEN jsonb_typeof(l.field_values -> 'length_cm') = 'number'
    THEN round((l.field_values ->> 'length_cm')::numeric, 1) > 0
      AND round((l.field_values ->> 'length_cm')::numeric, 1) <= 1000 END, false);

ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
UPDATE uploads SET purpose = 'catch_photo' WHERE purpose = 'log_photo';
UPDATE uploads SET attached_type = 'fishing_catch' WHERE attached_type = 'log_entry';
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'catch_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence'));
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'catch_photo' AND attached_type = 'fishing_catch')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo')
    OR (purpose = 'chat_photo' AND attached_type = 'channel_message')
    OR (purpose = 'dm_attachment' AND attached_type = 'dm_message')
    OR (purpose = 'report_evidence' AND attached_type = 'report'))
);

ALTER TABLE reports DROP CONSTRAINT reports_target_type;
UPDATE reports SET target_type = 'fishing_catch' WHERE target_type = 'log_entry';
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message', 'event_photo',
   'channel_message'));

INSERT INTO event_fishing (event_id, spot_visibility)
SELECT e.id,
  CASE WHEN EXISTS (
    SELECT 1 FROM event_log_entries AS l WHERE l.event_id = e.id AND l.location_visibility = 'participants'
  ) THEN 'participants' ELSE 'tayfa' END
FROM events AS e
WHERE e.type = 'fishing';

INSERT INTO fishing_catches (id, event_id, user_id, species, length_cm, lat, lng, visibility, client_request_id,
  deleted_at, deleted_by, created_at, photo_upload_id)
SELECT l.id, l.event_id, l.user_id, l.field_values ->> 'species', round((l.field_values ->> 'length_cm')::numeric, 1),
  l.lat, l.lng,
  CASE WHEN l.location_visibility IN ('participants', 'tayfa') THEN 'tayfa' ELSE 'self' END,
  l.client_request_id, l.deleted_at, l.deleted_by, l.created_at, l.photo_upload_id
FROM event_log_entries AS l;

UPDATE catalog_event_types SET definition = '{"components": [
    {"kind": "meeting_point"},
    {"kind": "log", "key": "catches", "labels": {"tr": "Av günlüğü"},
     "params": {
       "fields": [
         {"key": "species", "kind": "text", "required": true, "max_length": 60, "labels": {"tr": "Tür"}},
         {"key": "length_cm", "kind": "decimal", "required": true, "min": 0.1, "max": 1000, "decimals": 1,
          "labels": {"tr": "Boy (cm)"}}
       ],
       "location": true, "location_default": "self", "max_entries": 100}}
  ]}'::jsonb
WHERE key = 'fishing' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'fishing' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

DROP TABLE event_log_entries;
