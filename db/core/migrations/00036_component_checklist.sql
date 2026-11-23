-- Liste bileşeni (`checklist`; issue #241; ADR-0025 §4, §5, §10; ADR-0037;
-- ADR-0041 madde 3) ve kamp türünün Faz 1 yapısının kaldırılması.
--
--   event_checklist_items  bir etkinliğin bir liste bileşenindeki maddeler:
--                          ad (1–60), adet (1–999) ve isteğe bağlı üstlenen
--                          kişi (bileşenin `assignment` parametresiyle).
--                          Bir etkinlikte birden çok liste olabilir (tanımdaki
--                          `key`: gear, duty …); her madde (event_id,
--                          component_key) ile etkinliğe ve tür sürümündeki
--                          bileşene bağlıdır. Ortak tetikleyici
--                          (event_component_guard('checklist'), 00033)
--                          bileşeni olmayan ya da bileşeni kapalı etkinliğe
--                          yazmayı reddeder. Üstlenen ve ekleyen kişi kimliği
--                          taşır: hesap silmede boşaltılır.
--
-- Kamp türü tanımı (catalog_event_types ve catalog_event_type_versions,
-- camp v1) yerinde güncellenir (ADR-0041: geriye uyumluluk yok; issue #241
-- yorumu, #237): Faz 1 kamp ayrıntıları alan olur — site_name (metin, en
-- fazla 120), has_water ve has_toilet (evet/hayır), notes (uzun metin, en
-- fazla 1000); hepsi isteğe bağlı, sınırlar Faz 1 CHECK'leriyle aynı. Katalog
-- değiştiği için catalog_meta.catalog_version bir artar (istemci önbelleği).
--
-- VERİ TAŞIYAN VE TABLO SİLEN MIGRATION (hassas alan; ADR-0041 madde 3):
--   * `event_camp` satırlarının dolu değerleri `event_field_values`'a
--     kopyalanır (site_name, has_water, has_toilet, notes; NULL olan değer
--     yazılmaz). Alan tetikleyicisi (event_field_values_guard, 00031) tanım
--     güncellendikten sonra çalışır: alan etkinliğin tür sürümünde yoksa
--     migration hata verir ve hiçbir şey değişmez.
--   * `event_camp_items` satırları `event_checklist_items`'a kopyalanır (aynı
--     id, component_key = 'gear', updated_at = created_at). Kopya ortak
--     bileşen tetikleyicisinden geçer: kamp türünün etkinliğin bağlı olduğu
--     sürümünde `gear` anahtarlı liste yoksa migration hata verir (sessiz
--     veri kaybı olmaz). Kamp v1 tanımında vardır (00030).
--   * Ardından `event_camp_items` ve `event_camp` kaldırılır.
--
-- Geri alma planı: Down iki tabloyu Faz 1 şemasıyla (dizinler ve core_app
-- yetkileriyle) geri kurar, her kamp etkinliğine detay satırını alan
-- değerlerinden yazar, kamp etkinliklerinin `gear` listesindeki maddeleri geri
-- kopyalar, kamp etkinliklerinin bu dört alan değerini siler, alanları kamp
-- v1 tanımından çıkarır (catalog_version yine bir artar) ve bileşen tablosunu
-- kaldırır. Kamp dışındaki türlerin listeleri ve kampın `gear` dışındaki
-- listeleri Down ile kaybolur: Faz 1 şemasında yerleri yoktur. Uygulama kodu
-- da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_checklist_items (
  id               uuid PRIMARY KEY,
  event_id         uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'checklist').
  component_key    text NOT NULL,
  name             text NOT NULL,
  -- Bileşenin `quantity` parametresi kapalıysa hep 1 (uygulama denetler).
  quantity         integer NOT NULL DEFAULT 1,
  -- Maddeyi üstlenen kişi; bileşenin `assignment` parametresi kapalıysa hep
  -- NULL (uygulama denetler). Hesap silinince boşaltılır.
  assigned_user_id uuid,
  -- Maddeyi ekleyen kişi. Hesap silinince boşaltılır.
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_checklist_items_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_checklist_items_name_len CHECK (char_length(name) BETWEEN 1 AND 60),
  CONSTRAINT event_checklist_items_quantity CHECK (quantity BETWEEN 1 AND 999)
);
CREATE INDEX event_checklist_items_event_idx ON event_checklist_items (event_id, component_key, created_at, id);
-- Hesap silme.
CREATE INDEX event_checklist_items_assigned_user_id_idx ON event_checklist_items (assigned_user_id) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX event_checklist_items_created_by_idx ON event_checklist_items (created_by) WHERE created_by IS NOT NULL;
CREATE TRIGGER event_checklist_items_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_checklist_items
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('checklist');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): madde
-- silinir; yalnızca üstlenen kişisi değişir ve hesap silmede kişi alanları
-- boşaltılır. Madde başka etkinliğe ya da bileşene taşınamaz, adı ve adedi
-- değiştirilemez. SELECT ve INSERT burada da açıkça verilir: geri alınıp
-- yeniden uygulanan migration'ı başka bir rol çalıştırırsa varsayılan
-- yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_checklist_items TO core_app;
GRANT UPDATE (assigned_user_id, created_by, updated_at) ON event_checklist_items TO core_app;

-- Kamp v1 tanımına Faz 1 kamp ayrıntıları alan olarak eklenir.
UPDATE catalog_event_types SET definition = definition || '{"fields": [
    {"key": "site_name", "kind": "text", "max_length": 120, "labels": {"tr": "Kamp alanı"}},
    {"key": "has_water", "kind": "boolean", "labels": {"tr": "Su var"}},
    {"key": "has_toilet", "kind": "boolean", "labels": {"tr": "Tuvalet var"}},
    {"key": "notes", "kind": "long_text", "max_length": 1000, "labels": {"tr": "Notlar"}}
  ]}'::jsonb
WHERE key = 'camp' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'camp' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- Faz 1 verisi: kamp ayrıntıları alan değerlerine.
INSERT INTO event_field_values (event_id, field_key, value)
SELECT c.event_id, 'site_name', to_jsonb(c.site_name) FROM event_camp AS c WHERE c.site_name IS NOT NULL
UNION ALL
SELECT c.event_id, 'has_water', to_jsonb(c.has_water) FROM event_camp AS c WHERE c.has_water IS NOT NULL
UNION ALL
SELECT c.event_id, 'has_toilet', to_jsonb(c.has_toilet) FROM event_camp AS c WHERE c.has_toilet IS NOT NULL
UNION ALL
SELECT c.event_id, 'notes', to_jsonb(c.notes) FROM event_camp AS c WHERE c.notes IS NOT NULL;

-- Faz 1 verisi: kamp malzemeleri `gear` listesine.
INSERT INTO event_checklist_items (id, event_id, component_key, name, quantity, assigned_user_id, created_by, created_at, updated_at)
SELECT i.id, i.event_id, 'gear', i.name, i.quantity, i.assigned_user_id, i.created_by, i.created_at, i.created_at
FROM event_camp_items AS i;

-- Eski tablolar; dizinleri ve yetkileri tablolarla birlikte kalkar.
DROP TABLE event_camp_items;
DROP TABLE event_camp;

-- +goose Down

CREATE TABLE event_camp (
  event_id   uuid PRIMARY KEY,
  type       text NOT NULL DEFAULT 'camp',
  site_name  text,
  has_water  boolean,
  has_toilet boolean,
  notes      text,

  CONSTRAINT event_camp_type CHECK (type = 'camp'),
  CONSTRAINT event_camp_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_camp_site_name_len CHECK (site_name IS NULL OR char_length(site_name) BETWEEN 1 AND 120),
  CONSTRAINT event_camp_notes_len CHECK (notes IS NULL OR char_length(notes) <= 1000)
);

CREATE TABLE event_camp_items (
  id               uuid PRIMARY KEY,
  event_id         uuid NOT NULL REFERENCES event_camp (event_id) ON DELETE CASCADE,
  name             text NOT NULL,
  quantity         integer NOT NULL DEFAULT 1,
  assigned_user_id uuid,
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_camp_items_name_len CHECK (char_length(name) BETWEEN 1 AND 60),
  CONSTRAINT event_camp_items_quantity CHECK (quantity BETWEEN 1 AND 999)
);
CREATE INDEX event_camp_items_event_id_idx ON event_camp_items (event_id, created_at, id);
CREATE INDEX event_camp_items_assigned_user_id_idx ON event_camp_items (assigned_user_id) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX event_camp_items_created_by_idx ON event_camp_items (created_by) WHERE created_by IS NOT NULL;

GRANT SELECT, INSERT, UPDATE ON event_camp TO core_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON event_camp_items TO core_app;

INSERT INTO event_camp (event_id, site_name, has_water, has_toilet, notes)
SELECT e.id, s.value #>> '{}', (w.value #>> '{}')::boolean, (t.value #>> '{}')::boolean, n.value #>> '{}'
FROM events AS e
LEFT JOIN event_field_values AS s ON s.event_id = e.id AND s.field_key = 'site_name'
LEFT JOIN event_field_values AS w ON w.event_id = e.id AND w.field_key = 'has_water'
LEFT JOIN event_field_values AS t ON t.event_id = e.id AND t.field_key = 'has_toilet'
LEFT JOIN event_field_values AS n ON n.event_id = e.id AND n.field_key = 'notes'
WHERE e.type = 'camp';

INSERT INTO event_camp_items (id, event_id, name, quantity, assigned_user_id, created_by, created_at)
SELECT c.id, c.event_id, c.name, c.quantity, c.assigned_user_id, c.created_by, c.created_at
FROM event_checklist_items AS c
JOIN events AS e ON e.id = c.event_id
WHERE c.component_key = 'gear' AND e.type = 'camp';

DELETE FROM event_field_values AS f
USING events AS e
WHERE e.id = f.event_id AND e.type = 'camp'
  AND f.field_key IN ('site_name', 'has_water', 'has_toilet', 'notes');

UPDATE catalog_event_types SET definition = definition - 'fields'
WHERE key = 'camp' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'camp' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

DROP TABLE event_checklist_items;
