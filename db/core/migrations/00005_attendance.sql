-- Katılım doğrulama (check-in), organizatör düzeltmesi, yürüyüş yoklaması,
-- kalıcı katılım geçmişi ve av günlüğü (issue #50; veri modeli v14
-- "Konumla check-in", "Organizatör düzeltmesi", "Online etkinlik",
-- "Kalıcı katılım geçmişi", "Etkinlikte il/ilçe", "Yürüyüşte beklenen
-- dönüş", "Çevrimdışı yazma").
--
-- Check-in'de gönderilen konum hiçbir tabloya yazılmaz: mesafe uygulamada
-- (Go, haversine) hesaplanır ve yalnızca sonuç ile zaman tutulur
-- (ADR-0008, karar 2026-11-09 "PostGIS yok").
--
-- Kullanıcı kimlikleri identity'nin verisidir; veritabanları arası foreign
-- key yoktur (ADR-0009). "user → CASCADE" davranışı hesap silme olayının
-- tüketicisinde (internal/core/app/userevents) uygulanır.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Katılım kaydına eklenenler
-- ---------------------------------------------------------------------------

-- status_changed_at: yanıtın en son değiştiği an. Online etkinlikte
-- "Katıldım" yalnızca başlangıçtan önce "geliyorum" demiş kişiye açıktır;
-- updated_at yoklama değişikliklerinde de ilerlediği için bu ayrı tutulur.
-- check_in_attempts: konumla check-in denemeleri; etkinlik başına kişi
-- başına en fazla 5 (v14 "Konumla check-in").
ALTER TABLE event_rsvps
  ADD COLUMN status_changed_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN check_in_attempts integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT event_rsvps_check_in_attempts CHECK (check_in_attempts BETWEEN 0 AND 5),
  -- Katıldı işaretinin bir yöntemi vardır.
  ADD CONSTRAINT event_rsvps_attended_method CHECK (attendance <> 'attended' OR attendance_method IS NOT NULL);

-- +goose StatementBegin
CREATE FUNCTION event_rsvps_status_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    NEW.status_changed_at := now();
  ELSE
    NEW.status_changed_at := OLD.status_changed_at;
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_rsvps_status_changed BEFORE UPDATE ON event_rsvps
  FOR EACH ROW EXECUTE FUNCTION event_rsvps_status_changed();

-- Ortak etkinlik geçmişi (v7) ve profil geçmişi için.
CREATE INDEX event_rsvps_attended_idx ON event_rsvps (user_id, event_id) WHERE attendance = 'attended';

-- ---------------------------------------------------------------------------
-- Tayfa aktivitesi: last_activity_at, pasif tayfa temizliğinin dayanağıdır
-- (v14 "Tayfa yönetimi"). Yalnızca bu sütunun değişmesi tayfanın
-- updated_at'ini (istemcinin gördüğü update_time) ilerletmez.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION tayfas_set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - 'last_activity_at' - 'updated_at') = (to_jsonb(OLD) - 'last_activity_at' - 'updated_at') THEN
    NEW.updated_at := OLD.updated_at;
  ELSE
    NEW.updated_at := now();
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
DROP TRIGGER tayfas_updated_at ON tayfas;
CREATE TRIGGER tayfas_updated_at BEFORE UPDATE ON tayfas FOR EACH ROW EXECUTE FUNCTION tayfas_set_updated_at();

-- ---------------------------------------------------------------------------
-- Yürüyüş yoklaması (v14 "Yürüyüşte beklenen dönüş"). İstemci zamanı
-- (started_at/returned_at) ve sunucunun aldığı zaman (*_received_at) ayrı
-- tutulur (v14 "Çevrimdışı yazma"). Kişi başına bir satır; tekrar gönderim
-- birincil anahtarla zararsızdır. Gecikme alarmı (alert_sent_at) ayrı
-- issue'dadır.
-- ---------------------------------------------------------------------------

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
  -- İstemci zamanı gelecekte olamaz, 48 saatten eski olamaz; uygulama
  -- birkaç dakikalık saat kaymasına izin verir.
  CONSTRAINT event_hike_checkins_started_at CHECK (started_at BETWEEN started_received_at - interval '48 hours' AND started_received_at + interval '5 minutes'),
  CONSTRAINT event_hike_checkins_returned_at CHECK (returned_at IS NULL OR returned_at BETWEEN returned_received_at - interval '48 hours' AND returned_received_at + interval '5 minutes')
);
CREATE INDEX event_hike_checkins_user_id_idx ON event_hike_checkins (user_id);
-- Gecikme alarmı işi.
CREATE INDEX event_hike_checkins_out_idx ON event_hike_checkins (event_id) WHERE returned_at IS NULL;
CREATE TRIGGER event_hike_checkins_updated_at BEFORE UPDATE ON event_hike_checkins FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Kalıcı katılım geçmişi (v14 "Kalıcı katılım geçmişi"). Etkinlikten
-- bağımsız anlık görüntü: tür, mod, başlık, il/ilçe, süre. Koordinat asla
-- yazılmaz. Katılım kaldırılırsa kayıt silinir; etkinlik düzenlenirse
-- düzeltme penceresi (bitişten sonra 7 gün) boyunca güncellenir, sonra
-- donar. Etkinlik ya da tayfa silinse de kayıt kalır (SET NULL); kişinin
-- hesabı silinince kayıt silinir (userevents).
-- ---------------------------------------------------------------------------

CREATE TABLE user_activity_records (
  id                uuid PRIMARY KEY,
  user_id           uuid NOT NULL,
  event_id          uuid REFERENCES events (id) ON DELETE SET NULL,
  tayfa_id          uuid REFERENCES tayfas (id) ON DELETE SET NULL,
  event_type        text NOT NULL,
  mode              text NOT NULL,
  event_title       text NOT NULL,
  city              text,
  district          text,
  started_at        timestamptz NOT NULL,
  duration_minutes  integer NOT NULL,
  attendance_method text NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_activity_records_event_type CHECK (event_type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general')),
  CONSTRAINT user_activity_records_mode CHECK (mode IN ('in_person', 'online')),
  CONSTRAINT user_activity_records_event_title_len CHECK (char_length(event_title) BETWEEN 1 AND 120),
  CONSTRAINT user_activity_records_city_len CHECK (city IS NULL OR char_length(city) BETWEEN 1 AND 50),
  CONSTRAINT user_activity_records_district_len CHECK (district IS NULL OR char_length(district) BETWEEN 1 AND 50),
  -- 30 günlük etkinlikte gün başına 12 saat üst sınır: en fazla 360 saat.
  CONSTRAINT user_activity_records_duration CHECK (duration_minutes BETWEEN 0 AND 21600),
  CONSTRAINT user_activity_records_attendance_method CHECK (attendance_method IN ('location', 'online', 'hike_start', 'organizer')),
  CONSTRAINT user_activity_records_user_event_key UNIQUE (user_id, event_id)
);
CREATE INDEX user_activity_records_user_started_idx ON user_activity_records (user_id, started_at DESC);
CREATE INDEX user_activity_records_event_id_idx ON user_activity_records (event_id) WHERE event_id IS NOT NULL;
CREATE INDEX user_activity_records_tayfa_id_idx ON user_activity_records (tayfa_id) WHERE tayfa_id IS NOT NULL;
CREATE TRIGGER user_activity_records_updated_at BEFORE UPDATE ON user_activity_records FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Av günlüğü. Konum isteğe bağlıdır ve bilerek saklanır (kişi avın yerini
-- kaydeder); log'a, denetim kaydına ve olaylara yazılmaz. Tam konum
-- visibility = 'self' ise yalnızca sahibine, 'tayfa' ise etkinliğin
-- buluşma noktasını görebilenlere döner.
-- ---------------------------------------------------------------------------

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

-- +goose Down
DROP TABLE fishing_catches;
DROP TABLE user_activity_records;
DROP TABLE event_hike_checkins;
DROP TRIGGER tayfas_updated_at ON tayfas;
CREATE TRIGGER tayfas_updated_at BEFORE UPDATE ON tayfas FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP FUNCTION tayfas_set_updated_at();
DROP INDEX event_rsvps_attended_idx;
DROP TRIGGER event_rsvps_status_changed ON event_rsvps;
DROP FUNCTION event_rsvps_status_changed();
ALTER TABLE event_rsvps
  DROP CONSTRAINT event_rsvps_attended_method,
  DROP CONSTRAINT event_rsvps_check_in_attempts,
  DROP COLUMN check_in_attempts,
  DROP COLUMN status_changed_at;
