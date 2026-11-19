-- Ölçüm bileşeni (`measure`; issue #245; ADR-0025 §4, §5, §11; ADR-0037 §2,
-- §3).
--
--   event_measurements  bir etkinliğin bir ölçüm bileşenindeki değerler.
--                       Bileşenin `scope` parametresi `total` ise etkinlik ve
--                       bileşen başına tek satır (user_id NULL); `personal`
--                       ise kişi başına bir satır (user_id: değerin ait olduğu
--                       katılımcı). Değeri kimin girdiği (`entered_by`
--                       parametresi: katılımcı kendi adına ya da organizatör
--                       ve atadığı hakemler) uygulama katmanında denetlenir;
--                       satır son yazanı (entered_by) tutar. Birim, sınırlar
--                       ve ondalık basamak tür sürümündeki tanımdan gelir;
--                       burada yalnızca genel sınır (±1 000 000 000, üç
--                       basamak) vardır.
--
-- Tablo (event_id, component_key) ile etkinliğe ve tür sürümündeki bileşene
-- bağlıdır; ortak tetikleyici (event_component_guard('measure'), 00033)
-- bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Önerilen değer (`prefill_from`, ADR-0037 §2) tabloya yazılmaz: okunurken
-- etkinliğin alan değerinden hesaplanır. Organizatör onaylayınca ya da
-- düzeltince satır olur; onaylanmamış öneri istatistiğe girmez.
--
-- Kişisel veri (ADR-0025 §11): kişisel değer kişiye aittir, dışa aktarmaya
-- girer ve hesap silmede silinir. Toplam değer etkinliğindir; hesap silmede
-- yalnızca yazan (entered_by) boşalır.
--
-- Doğa yürüyüşü türü tanımına (catalog_event_types ve
-- catalog_event_type_versions, hike v1) Notion Faz 3 kataloğundaki iki toplam
-- ölçüm yerinde eklenir (ADR-0041): `distance` (km, 0–1000, iki basamak,
-- planlanan mesafeden önerilir) ve `elevation_gain` (m, 0–9000, planlanan
-- irtifadan önerilir); ikisini de organizatör girer, vitrine çıkabilir. Katalog
-- değiştiği için catalog_meta.catalog_version bir artar. Veri taşınmaz ve
-- silinmez.
--
-- Geri alma planı: Down iki ölçümü hike v1 tanımından çıkarır
-- (catalog_version yine bir artar) ve tabloyu kaldırır; girilmiş ölçümler
-- kaybolur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_measurements (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki ölçümün anahtarı (ölçümün key'i zorunludur).
  component_key text NOT NULL,
  -- Kişisel ölçümde değerin ait olduğu katılımcı; toplam ölçümde NULL.
  -- Hesap silinince satır silinir.
  user_id       uuid,
  value         numeric(13, 3) NOT NULL,
  -- Değeri son yazan kişi. Hesap silinince boşaltılır.
  entered_by    uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_measurements_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_measurements_value CHECK (value BETWEEN -1000000000 AND 1000000000)
);
-- Toplam ölçüm etkinlik ve bileşen başına tek; kişisel ölçüm kişi başına tek.
CREATE UNIQUE INDEX event_measurements_total_idx ON event_measurements (event_id, component_key) WHERE user_id IS NULL;
CREATE UNIQUE INDEX event_measurements_personal_idx ON event_measurements (event_id, component_key, user_id) WHERE user_id IS NOT NULL;
-- Dışa aktarma ve hesap silme.
CREATE INDEX event_measurements_user_id_idx ON event_measurements (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX event_measurements_entered_by_idx ON event_measurements (entered_by) WHERE entered_by IS NOT NULL;
CREATE TRIGGER event_measurements_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_measurements
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('measure');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): değer
-- değişir (ON CONFLICT) ve kaldırılır; hesap silmede yazan boşaltılır. Satır
-- başka etkinliğe, bileşene ya da kişiye taşınamaz. SELECT ve INSERT burada da
-- açıkça verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_measurements TO core_app;
GRANT UPDATE (value, entered_by, updated_at) ON event_measurements TO core_app;

-- Hike v1 tanımına iki toplam ölçüm eklenir (Notion Faz 3 kataloğu: ölçüm
-- distance km T org vit, ölçüm elevation_gain m T org vit; plan alanı →
-- ölçüm).
UPDATE catalog_event_types SET definition = jsonb_set(definition, '{components}',
  COALESCE(definition -> 'components', '[]'::jsonb) || '[
    {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {
      "unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 1000, "decimals": 2,
      "showcase": true, "prefill_from": "planned_distance"}},
    {"kind": "measure", "key": "elevation_gain", "labels": {"tr": "İrtifa kazancı"}, "params": {
      "unit": "m", "scope": "total", "entered_by": "organizer", "min": 0, "max": 9000, "decimals": 0,
      "showcase": true, "prefill_from": "planned_elevation"}}
  ]'::jsonb)
WHERE key = 'hike' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'hike' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- +goose Down

UPDATE catalog_event_types SET definition = jsonb_set(definition, '{components}', COALESCE((
    SELECT jsonb_agg(c ORDER BY n) FROM jsonb_array_elements(definition -> 'components') WITH ORDINALITY AS x(c, n)
    WHERE c ->> 'kind' <> 'measure'), '[]'::jsonb))
WHERE key = 'hike' AND version = 1;
UPDATE catalog_event_type_versions AS v SET definition = t.definition
FROM catalog_event_types AS t
WHERE t.key = 'hike' AND v.type_key = t.key AND v.version = 1 AND t.version = 1;
UPDATE catalog_meta SET catalog_version = catalog_version + 1;

DROP TABLE event_measurements;
