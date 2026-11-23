-- Araçlar ve koltuk paylaşımı bileşeni (`vehicles`; issue #240; ADR-0025 §4,
-- §10; ADR-0037 §3; ADR-0041 madde 3).
--
--   event_vehicles   bir etkinliğin bir araçlar bileşenine katılımcıların
--                    yazdığı araçlar: kişi başına bir araç (marka, model,
--                    yıl, renk, isteğe bağlı plaka) ve `seats` parametresi
--                    açıksa araç sahibinin yazdığı boş koltuk sayısı (0–8).
--                    Para ya da masraf alanı yoktur (ADR-0037 §3). Plaka
--                    kişisel veridir: yalnızca etkinliğin katılımcılarına
--                    döner ve etkinlik bitiminden 30 gün sonra temizlenir
--                    (ClearOldPlates; ADR-0025 "Plaka kuralı").
--   event_vehicle_seat_requests
--                    "bu araçla gelmek istiyorum" isteği: kişi başına
--                    bileşende tek istek; araç sahibi onaylar (`approved`).
--                    Araç silinince istekleri de gider.
--
-- İki tablo da (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('vehicles'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
-- İkisi de kişi kimliği taşır: hesap silmeye ve dışa aktarmaya girer.
--
-- VERİ TAŞIYAN VE TABLO SİLEN MIGRATION (hassas alan; ADR-0041 madde 3):
--   * Faz 1'in `event_convoy_vehicles` satırları `event_vehicles`'a
--     kopyalanır (aynı id, component_key = 'vehicles', seats = 0,
--     updated_at = created_at). Kopya ortak tetikleyiciden geçer: konvoy
--     türünün etkinliğin bağlı olduğu sürümünde `vehicles` bileşeni yoksa
--     migration hata verir ve hiçbir şey değişmez (sessiz veri kaybı olmaz).
--   * Ardından `event_convoy_vehicles` ve artık hiçbir veri taşımayan
--     `event_convoy` (yalnızca event_id, type) tabloları kaldırılır.
--
-- Geri alma planı: Down iki tabloyu Faz 1 şemasıyla (dizinler ve core_app
-- yetkileriyle) geri kurar, her konvoy etkinliğine detay satırını yazar,
-- konvoy etkinliklerinin `vehicles` anahtarlı araçlarını geri kopyalar ve
-- bileşen tablolarını kaldırır. Koltuk sayıları, koltuk istekleri ve konvoy
-- dışındaki türlerin araçları Down ile kaybolur: Faz 1 şemasında yerleri
-- yoktur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_vehicles (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'vehicles').
  component_key text NOT NULL,
  -- Aracın sahibi. Hesap silinince satır silinir.
  user_id       uuid NOT NULL,
  -- Üye garajındaki araç (ADR-0040); garaj modülü gelene kadar hep NULL.
  vehicle_id    uuid,
  make          text NOT NULL,
  model         text,
  model_year    integer,
  color         text,
  -- Kişisel veri; yalnızca bileşenin `plate` parametresi açıksa yazılır
  -- (uygulama denetler) ve etkinlik bitiminden 30 gün sonra boşaltılır.
  plate         text,
  -- Boş koltuk sayısı; bileşenin `seats` parametresi kapalıysa hep 0
  -- (uygulama denetler). Onaylanan istek sayısı bunu aşamaz (uygulama,
  -- etkinlik satırı kilitliyken denetler).
  seats         integer NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_vehicles_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_vehicles_make_len CHECK (char_length(make) BETWEEN 1 AND 40),
  CONSTRAINT event_vehicles_model_len CHECK (model IS NULL OR char_length(model) BETWEEN 1 AND 40),
  -- Üst sınır uygulamada güncel yıl + 1'dir; CHECK sabit kalmalıdır.
  CONSTRAINT event_vehicles_model_year CHECK (model_year IS NULL OR model_year BETWEEN 1900 AND 2100),
  CONSTRAINT event_vehicles_color_len CHECK (color IS NULL OR char_length(color) BETWEEN 1 AND 30),
  CONSTRAINT event_vehicles_plate CHECK (plate IS NULL OR plate ~ '^[0-9A-Z][0-9A-Z ]{0,14}[0-9A-Z]$'),
  CONSTRAINT event_vehicles_seats CHECK (seats BETWEEN 0 AND 8),
  -- Bileşende kişi başına bir araç.
  CONSTRAINT event_vehicles_event_component_user_key UNIQUE (event_id, component_key, user_id),
  -- Koltuk isteğinin bileşik foreign key'i için.
  CONSTRAINT event_vehicles_id_event_component_key UNIQUE (id, event_id, component_key)
);
-- Hesap silme ve dışa aktarma.
CREATE INDEX event_vehicles_user_id_idx ON event_vehicles (user_id);
-- Plakası temizlenmemiş araçlar (plaka temizliği işi).
CREATE INDEX event_vehicles_plate_idx ON event_vehicles (event_id) WHERE plate IS NOT NULL;
CREATE TRIGGER event_vehicles_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_vehicles
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('vehicles');

CREATE TABLE event_vehicle_seat_requests (
  id               uuid PRIMARY KEY,
  event_id         uuid NOT NULL,
  component_key    text NOT NULL,
  -- İsteyen kişi. Hesap silinince satır silinir.
  user_id          uuid NOT NULL,
  -- İstenen araç (event_vehicles.id).
  event_vehicle_id uuid NOT NULL,
  status           text NOT NULL DEFAULT 'pending',
  created_at       timestamptz NOT NULL DEFAULT now(),
  -- Araç sahibinin onayladığı an; bekleyen istekte NULL.
  decided_at       timestamptz,

  -- Bileşende kişi başına tek istek: kişi tek araçla gelir.
  CONSTRAINT event_vehicle_seat_requests_event_component_user_key UNIQUE (event_id, component_key, user_id),
  CONSTRAINT event_vehicle_seat_requests_status CHECK (status IN ('pending', 'approved')),
  CONSTRAINT event_vehicle_seat_requests_decided CHECK ((status = 'approved') = (decided_at IS NOT NULL)),
  -- İstek, aracın etkinliğine ve bileşenine bağlıdır; araçla birlikte gider.
  CONSTRAINT event_vehicle_seat_requests_vehicle_fkey FOREIGN KEY (event_vehicle_id, event_id, component_key)
    REFERENCES event_vehicles (id, event_id, component_key) ON DELETE CASCADE
);
CREATE INDEX event_vehicle_seat_requests_vehicle_idx ON event_vehicle_seat_requests (event_vehicle_id);
-- Hesap silme ve dışa aktarma.
CREATE INDEX event_vehicle_seat_requests_user_id_idx ON event_vehicle_seat_requests (user_id);
CREATE TRIGGER event_vehicle_seat_requests_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_vehicle_seat_requests
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('vehicles');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): araç
-- değişir (ON CONFLICT) ve silinir; başka etkinliğe, bileşene ya da kişiye
-- taşınamaz. İstek yalnızca onaylanır ve silinir; başka araca ya da kişiye
-- taşınamaz.
-- SELECT ve INSERT burada da açıkça verilir: geri alınıp yeniden uygulanan
-- migration'ı başka bir rol çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_vehicles TO core_app;
GRANT UPDATE (make, model, model_year, color, plate, seats, updated_at) ON event_vehicles TO core_app;
GRANT SELECT, INSERT, DELETE ON event_vehicle_seat_requests TO core_app;
GRANT UPDATE (status, decided_at) ON event_vehicle_seat_requests TO core_app;

-- Faz 1 verisi: konvoy araçları bileşen tablosuna.
INSERT INTO event_vehicles (id, event_id, component_key, user_id, vehicle_id, make, model, model_year, color, plate, seats, created_at, updated_at)
SELECT v.id, v.event_id, 'vehicles', v.user_id, v.vehicle_id, v.make, v.model, v.model_year, v.color, v.plate, 0, v.created_at, v.created_at
FROM event_convoy_vehicles AS v;

-- Eski tablolar; dizinleri ve yetkileri tablolarla birlikte kalkar.
DROP TABLE event_convoy_vehicles;
DROP TABLE event_convoy;

-- +goose Down

CREATE TABLE event_convoy (
  event_id uuid PRIMARY KEY,
  type     text NOT NULL DEFAULT 'convoy',

  CONSTRAINT event_convoy_type CHECK (type = 'convoy'),
  CONSTRAINT event_convoy_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE
);

CREATE TABLE event_convoy_vehicles (
  id         uuid PRIMARY KEY,
  event_id   uuid NOT NULL REFERENCES event_convoy (event_id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  vehicle_id uuid,
  make       text NOT NULL,
  model      text,
  model_year integer,
  color      text,
  plate      text,
  created_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_convoy_vehicles_make_len CHECK (char_length(make) BETWEEN 1 AND 40),
  CONSTRAINT event_convoy_vehicles_model_len CHECK (model IS NULL OR char_length(model) BETWEEN 1 AND 40),
  CONSTRAINT event_convoy_vehicles_model_year CHECK (model_year IS NULL OR model_year BETWEEN 1900 AND 2100),
  CONSTRAINT event_convoy_vehicles_color_len CHECK (color IS NULL OR char_length(color) BETWEEN 1 AND 30),
  CONSTRAINT event_convoy_vehicles_plate CHECK (plate IS NULL OR plate ~ '^[0-9A-Z][0-9A-Z ]{0,14}[0-9A-Z]$'),
  CONSTRAINT event_convoy_vehicles_event_user_key UNIQUE (event_id, user_id)
);
CREATE INDEX event_convoy_vehicles_user_id_idx ON event_convoy_vehicles (user_id);
CREATE INDEX event_convoy_vehicles_plate_idx ON event_convoy_vehicles (event_id) WHERE plate IS NOT NULL;

GRANT SELECT, INSERT, UPDATE ON event_convoy TO core_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON event_convoy_vehicles TO core_app;

INSERT INTO event_convoy (event_id)
SELECT e.id FROM events AS e WHERE e.type = 'convoy';

INSERT INTO event_convoy_vehicles (id, event_id, user_id, vehicle_id, make, model, model_year, color, plate, created_at)
SELECT v.id, v.event_id, v.user_id, v.vehicle_id, v.make, v.model, v.model_year, v.color, v.plate, v.created_at
FROM event_vehicles AS v
JOIN events AS e ON e.id = v.event_id
WHERE v.component_key = 'vehicles' AND e.type = 'convoy';

DROP TABLE event_vehicle_seat_requests;
DROP TABLE event_vehicles;
