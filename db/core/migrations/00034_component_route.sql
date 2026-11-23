-- Rota bileşeni (`route`; issue #239; ADR-0025 §4, §10; ADR-0041 madde 3).
--
--   event_routes   bir etkinliğin bir rota bileşeninin güzergâhı: sıralı
--                  nokta dizisi (jsonb; {"lat": …, "lng": …}, 2–500 nokta;
--                  is_valid_route, 00004). Rotası olmayan bileşenin satırı
--                  yoktur. Satır kişi kimliği taşımaz: rota, organizatörün
--                  bilerek paylaştığı içeriktir (ADR-0008), kişisel veri
--                  değildir; dışa aktarmaya ve hesap silmeye girmez.
--                  Satır (event_id, component_key) ile etkinliğe ve tür
--                  sürümündeki bileşene bağlıdır; ortak tetikleyici
--                  (event_component_guard('route'), 00033) bileşeni olmayan
--                  ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- VERİ TAŞIYAN VE KOLON SİLEN MIGRATION (hassas alan; ADR-0041 madde 3):
--   * Faz 1'in `event_convoy.route` değerleri `event_routes`'a kopyalanır
--     (component_key = 'route'; zamanlar etkinliğin created_at / updated_at
--     değerleridir). Kopya ortak tetikleyiciden geçer: konvoy türünün
--     etkinliğin bağlı olduğu sürümünde `route` bileşeni yoksa migration
--     hata verir ve hiçbir şey değişmez (sessiz veri kaybı olmaz).
--   * Ardından `event_convoy.route` kolonu (ve event_convoy_route kısıtı)
--     kaldırılır. `event_convoy` tablosu, araçlar ona bağlı olduğu için
--     kalır; `vehicles` bileşeni (#240) taşıyıp kaldırır.
--   * is_valid_route fonksiyonu kalır; artık event_routes kullanır.
--
-- Geri alma planı: Down, `event_convoy.route` kolonunu ve kısıtını geri
-- ekler, konvoy etkinliklerinin `route` anahtarlı rotalarını geri kopyalar
-- ve event_routes tablosunu kaldırır. Konvoy dışındaki türlerin (ve konvoyda
-- `route` dışındaki anahtarların) rotaları Down ile kaybolur: Faz 1 şemasında
-- yerleri yoktur. Uygulama kodu da birlikte geri alınmalıdır (eski kod
-- kolonu, yeni kod tabloyu okur).

-- +goose Up

CREATE TABLE event_routes (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'route').
  component_key text NOT NULL,
  -- Sıralı noktalar; bkz. is_valid_route (2–500 nokta, yalnızca lat ve lng).
  points        jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Rotanın son yazıldığı an; uygulama yazar (isteğin zamanı).
  updated_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_routes_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_routes_points CHECK (is_valid_route(points))
);
CREATE TRIGGER event_routes_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_routes
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('route');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): rota
-- değişir (ON CONFLICT) ve kaldırılır; satır başka etkinliğe ya da bileşene
-- taşınamaz.
GRANT UPDATE (points, updated_at) ON event_routes TO core_app;
GRANT DELETE ON event_routes TO core_app;

-- Faz 1 verisi: konvoy rotaları bileşen tablosuna.
INSERT INTO event_routes (event_id, component_key, points, created_at, updated_at)
SELECT c.event_id, 'route', c.route, e.created_at, e.updated_at
FROM event_convoy AS c
JOIN events AS e ON e.id = c.event_id
WHERE c.route IS NOT NULL;

-- Eski kolon; event_convoy_route kısıtı kolonla birlikte kalkar.
ALTER TABLE event_convoy DROP COLUMN route;

-- +goose Down

ALTER TABLE event_convoy ADD COLUMN route jsonb;
ALTER TABLE event_convoy ADD CONSTRAINT event_convoy_route CHECK (is_valid_route(route));

UPDATE event_convoy AS c SET route = r.points
FROM event_routes AS r
WHERE r.event_id = c.event_id AND r.component_key = 'route';

DROP TABLE event_routes;
