-- Etkinlik çekirdeği (issue #49; veri modeli v14 "Duyuru ve Etkinlik",
-- "Türe özel tablolar", "Etkinlik türü bütünlüğü", "Etkinlik iptali",
-- "Online etkinlik", "Etkinlikte il/ilçe", "Konvoy aracı" ve 2026-11-09
-- kararları).
--
-- PostGIS yok (2026-11-09): konumlar enlem/boylam sütunlarıdır (CHECK ile
-- aralık), konvoy rotası jsonb nokta dizisidir ({"lat": …, "lng": …},
-- 2–500 nokta). Mesafe hesabı uygulamada (Go) yapılır.
--
-- Etkinlik açma (2026-11-09): varsayılan yönetici ve moderatör;
-- tayfas.members_can_create_events açıksa her aktif üye. Kural uygulamadaki
-- izin tablosundadır (internal/core/app/authz).
--
-- Kullanıcı kimlikleri (created_by, user_id, cancelled_by …) identity'nin
-- verisidir; veritabanları arası foreign key yoktur (ADR-0009).
--
-- Bu sürümde yok: fishing_catches, event_hike_checkins, turnuva tabloları
-- (ayrı issue'lar). event_series ve events.series_id şimdiden açılır; seri
-- üretimi ayrı iştir.

-- +goose Up

ALTER TABLE tayfas ADD COLUMN members_can_create_events boolean NOT NULL DEFAULT false;

-- Konvoy rotası: 2–500 noktalık dizi; her nokta yalnızca sayısal lat
-- (-90..90) ve lng (-180..180) alanları olan bir nesnedir. Uygulama aynı
-- kuralı Go'da da uygular; bu fonksiyon ikinci savunma hattıdır.
-- +goose StatementBegin
CREATE FUNCTION is_valid_route(r jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
  p jsonb;
BEGIN
  IF r IS NULL THEN
    RETURN true;
  END IF;
  IF jsonb_typeof(r) <> 'array' THEN
    RETURN false;
  END IF;
  IF jsonb_array_length(r) NOT BETWEEN 2 AND 500 THEN
    RETURN false;
  END IF;
  -- Kontroller sırayla yapılır: nesne olmayan bir öğede jsonb_object_keys
  -- hata verir.
  FOR p IN SELECT jsonb_array_elements(r) LOOP
    IF jsonb_typeof(p) <> 'object' THEN
      RETURN false;
    END IF;
    IF jsonb_typeof(p -> 'lat') IS DISTINCT FROM 'number'
       OR jsonb_typeof(p -> 'lng') IS DISTINCT FROM 'number' THEN
      RETURN false;
    END IF;
    IF (SELECT count(*) FROM jsonb_object_keys(p)) <> 2 THEN
      RETURN false;
    END IF;
    -- jsonb sayıları numeric'tir; numeric'e dönüşüm taşmaz.
    IF (p ->> 'lat')::numeric NOT BETWEEN -90 AND 90
       OR (p ->> 'lng')::numeric NOT BETWEEN -180 AND 180 THEN
      RETURN false;
    END IF;
  END LOOP;
  RETURN true;
END
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Tekrarlayan etkinlik serisi (v14 "Tekrarlayan etkinlik"). Üretim işi ayrı
-- issue'dadır; tablo, events.series_id için şimdiden vardır.
-- ---------------------------------------------------------------------------

CREATE TABLE event_series (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Foreign key aşağıda, events oluşturulduktan sonra eklenir.
  template_event_id uuid,
  -- RFC 5545 RRULE.
  rrule             text NOT NULL,
  -- IANA saat dilimi (yaz saati kaymaları için).
  timezone          text NOT NULL,
  generated_until   timestamptz,
  ended_at          timestamptz,
  created_by        uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_series_rrule_len CHECK (char_length(rrule) BETWEEN 1 AND 500),
  CONSTRAINT event_series_timezone_len CHECK (char_length(timezone) BETWEEN 1 AND 64),
  -- events'in bileşik foreign key'i için: seri ile etkinlik aynı tayfadadır.
  CONSTRAINT event_series_tayfa_id_id_key UNIQUE (tayfa_id, id)
);
CREATE INDEX event_series_template_event_id_idx ON event_series (template_event_id) WHERE template_event_id IS NOT NULL;
CREATE INDEX event_series_created_by_idx ON event_series (created_by) WHERE created_by IS NOT NULL;
CREATE TRIGGER event_series_updated_at BEFORE UPDATE ON event_series FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Etkinlik
-- ---------------------------------------------------------------------------

CREATE TABLE events (
  id                 uuid PRIMARY KEY,
  tayfa_id           uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  type               text NOT NULL,
  mode               text NOT NULL,
  title              text NOT NULL,
  description        text,
  starts_at          timestamptz NOT NULL,
  ends_at            timestamptz NOT NULL,
  -- Yüz yüze etkinlikte buluşma noktası. Konum log'a yazılmaz (ADR-0008).
  meeting_point_name text,
  meeting_lat        double precision,
  meeting_lng        double precision,
  city               text,
  district           text,
  -- Online etkinlikte bağlantı; yalnızca https. Yalnızca aktif üyelere döner.
  online_url         text,
  -- NULL: sınırsız.
  capacity           integer,
  status             text NOT NULL DEFAULT 'scheduled',
  cancelled_at       timestamptz,
  -- İptal eden; hesabı silinirse NULL.
  cancelled_by       uuid,
  cancel_reason      text,
  series_id          uuid,
  -- Seri örneği elle düzenlendi; seri güncellemeleri bu örneğe dokunmaz.
  series_override    boolean NOT NULL DEFAULT false,
  -- Keşfet (Faz 2) için; şimdilik yalnızca saklanır.
  discoverable       boolean NOT NULL DEFAULT false,
  -- Organizatör; hesabı silinirse NULL.
  created_by         uuid,
  -- Aynı oluşturma isteğinin tekrar gönderilmesine karşı (CLAUDE.md).
  client_request_id  uuid,
  deleted_at         timestamptz,
  deleted_by         uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT events_type CHECK (type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general')),
  CONSTRAINT events_mode CHECK (mode IN ('in_person', 'online')),
  CONSTRAINT events_title_len CHECK (char_length(title) BETWEEN 1 AND 120),
  CONSTRAINT events_description_len CHECK (description IS NULL OR char_length(description) <= 5000),
  CONSTRAINT events_time CHECK (ends_at > starts_at),
  CONSTRAINT events_meeting_point_name_len CHECK (meeting_point_name IS NULL OR char_length(meeting_point_name) BETWEEN 1 AND 120),
  CONSTRAINT events_meeting_lat CHECK (meeting_lat IS NULL OR meeting_lat BETWEEN -90 AND 90),
  CONSTRAINT events_meeting_lng CHECK (meeting_lng IS NULL OR meeting_lng BETWEEN -180 AND 180),
  CONSTRAINT events_meeting_point CHECK ((meeting_lat IS NULL) = (meeting_lng IS NULL)),
  CONSTRAINT events_city_len CHECK (city IS NULL OR char_length(city) BETWEEN 1 AND 50),
  CONSTRAINT events_district_len CHECK (district IS NULL OR char_length(district) BETWEEN 1 AND 50),
  CONSTRAINT events_online_url CHECK (online_url IS NULL OR (online_url LIKE 'https://_%' AND char_length(online_url) <= 2048)),
  -- Yüz yüze: buluşma noktası, il ve ilçe zorunlu, bağlantı yok. Online:
  -- bağlantı zorunlu, konum alanları boş (v14 "Online etkinlik",
  -- "Etkinlikte il/ilçe").
  CONSTRAINT events_mode_fields CHECK (
    (mode = 'in_person' AND meeting_lat IS NOT NULL AND city IS NOT NULL AND district IS NOT NULL AND online_url IS NULL) OR
    (mode = 'online' AND online_url IS NOT NULL AND meeting_lat IS NULL AND meeting_point_name IS NULL AND city IS NULL AND district IS NULL)
  ),
  CONSTRAINT events_capacity CHECK (capacity IS NULL OR capacity BETWEEN 1 AND 10000),
  CONSTRAINT events_status CHECK (status IN ('scheduled', 'cancelled')),
  -- İptal silme değildir; iptal zamanı ve nedeni tutulur (v14 "Etkinlik
  -- iptali").
  CONSTRAINT events_cancellation CHECK (
    (status = 'scheduled' AND cancelled_at IS NULL AND cancelled_by IS NULL AND cancel_reason IS NULL) OR
    (status = 'cancelled' AND cancelled_at IS NOT NULL)
  ),
  CONSTRAINT events_cancel_reason_len CHECK (cancel_reason IS NULL OR char_length(cancel_reason) BETWEEN 1 AND 500),
  CONSTRAINT events_series_override CHECK (NOT series_override OR series_id IS NOT NULL),
  CONSTRAINT events_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  -- Türe özel tabloların bileşik foreign key'i için (v14 "Etkinlik türü
  -- bütünlüğü").
  CONSTRAINT events_id_type_key UNIQUE (id, type),
  CONSTRAINT events_series_fkey FOREIGN KEY (tayfa_id, series_id)
    REFERENCES event_series (tayfa_id, id) ON DELETE SET NULL (series_id)
);
-- Yaklaşan etkinlikler.
CREATE INDEX events_tayfa_id_starts_at_idx ON events (tayfa_id, starts_at, id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX events_client_request_key ON events (created_by, client_request_id) WHERE client_request_id IS NOT NULL;
CREATE INDEX events_created_by_idx ON events (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX events_series_id_idx ON events (series_id) WHERE series_id IS NOT NULL;
-- 30 günlük kalıcı silme işi.
CREATE INDEX events_deleted_at_idx ON events (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER events_updated_at BEFORE UPDATE ON events FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE event_series ADD CONSTRAINT event_series_template_event_fkey
  FOREIGN KEY (template_event_id) REFERENCES events (id) ON DELETE SET NULL;

-- Katılım. Kapasite doluyken "geliyorum" bekleme listesine (waitlist) düşer;
-- yer açılınca en eski bekleme kaydı aynı transaction'da going olur
-- (v14 "Bekleme listesi terfisi"). Sıra updated_at ile belirlenir.
CREATE TABLE event_rsvps (
  event_id             uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  user_id              uuid NOT NULL,
  status               text NOT NULL,
  attendance           text NOT NULL DEFAULT 'unknown',
  attendance_method    text,
  checked_in_at        timestamptz,
  attendance_marked_by uuid,
  attendance_marked_at timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, user_id),
  CONSTRAINT event_rsvps_status CHECK (status IN ('going', 'maybe', 'not_going', 'waitlist')),
  CONSTRAINT event_rsvps_attendance CHECK (attendance IN ('unknown', 'attended', 'no_show')),
  CONSTRAINT event_rsvps_attendance_method CHECK (attendance_method IS NULL OR attendance_method IN ('location', 'online', 'hike_start', 'organizer')),
  CONSTRAINT event_rsvps_attendance_marked CHECK (attendance_marked_by IS NULL OR attendance_marked_at IS NOT NULL)
);
CREATE INDEX event_rsvps_user_id_idx ON event_rsvps (user_id);
-- Katılım listesi ve bekleme sırası.
CREATE INDEX event_rsvps_event_status_idx ON event_rsvps (event_id, status, updated_at, user_id);
CREATE TRIGGER event_rsvps_updated_at BEFORE UPDATE ON event_rsvps FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Türe özel tablolar (1:1). Her biri sabit bir type sütunuyla
-- events (id, type)'a bağlanır: yanlış türdeki etkinliğe detay eklenemez.
-- ---------------------------------------------------------------------------

CREATE TABLE event_convoy (
  event_id uuid PRIMARY KEY,
  type     text NOT NULL DEFAULT 'convoy',
  -- NULL: rota yok. Bkz. is_valid_route.
  route    jsonb,

  CONSTRAINT event_convoy_type CHECK (type = 'convoy'),
  CONSTRAINT event_convoy_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_convoy_route CHECK (is_valid_route(route))
);

-- Konvoya katılan araç (v14 "Konvoy aracı"). Plaka kişisel veridir: isteğe
-- bağlıdır, yalnızca aynı etkinliğin katılımcılarına döner, etkinlik
-- bitiminden 30 gün sonra temizlenir (temizlik işi ayrı issue).
CREATE TABLE event_convoy_vehicles (
  id         uuid PRIMARY KEY,
  event_id   uuid NOT NULL REFERENCES event_convoy (event_id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  -- Faz 2 garaj tablosu; şimdilik bağlantısız.
  vehicle_id uuid,
  make       text NOT NULL,
  model      text,
  model_year integer,
  color      text,
  plate      text,
  created_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_convoy_vehicles_make_len CHECK (char_length(make) BETWEEN 1 AND 40),
  CONSTRAINT event_convoy_vehicles_model_len CHECK (model IS NULL OR char_length(model) BETWEEN 1 AND 40),
  -- Üst sınır uygulamada güncel yıl + 1'dir; CHECK sabit kalmalıdır.
  CONSTRAINT event_convoy_vehicles_model_year CHECK (model_year IS NULL OR model_year BETWEEN 1900 AND 2100),
  CONSTRAINT event_convoy_vehicles_color_len CHECK (color IS NULL OR char_length(color) BETWEEN 1 AND 30),
  CONSTRAINT event_convoy_vehicles_plate CHECK (plate IS NULL OR plate ~ '^[0-9A-Z][0-9A-Z ]{0,14}[0-9A-Z]$'),
  -- Kişi başına bir araç.
  CONSTRAINT event_convoy_vehicles_event_user_key UNIQUE (event_id, user_id)
);
CREATE INDEX event_convoy_vehicles_user_id_idx ON event_convoy_vehicles (user_id);

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

CREATE TABLE event_fishing (
  event_id        uuid PRIMARY KEY,
  type            text NOT NULL DEFAULT 'fishing',
  -- Mera konumunu kim görür: tayfanın tüm aktif üyeleri ya da yalnızca
  -- katılımcılar (going/maybe), organizatör, moderatör ve yönetici.
  spot_visibility text NOT NULL DEFAULT 'tayfa',

  CONSTRAINT event_fishing_type CHECK (type = 'fishing'),
  CONSTRAINT event_fishing_event_fkey FOREIGN KEY (event_id, type) REFERENCES events (id, type) ON DELETE CASCADE,
  CONSTRAINT event_fishing_spot_visibility CHECK (spot_visibility IN ('tayfa', 'participants'))
);

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

-- +goose Down
DROP TABLE event_sports;
DROP TABLE event_gaming;
DROP TABLE event_fishing;
DROP TABLE event_hike;
DROP TABLE event_camp_items;
DROP TABLE event_camp;
DROP TABLE event_convoy_vehicles;
DROP TABLE event_convoy;
DROP TABLE event_rsvps;
ALTER TABLE event_series DROP CONSTRAINT event_series_template_event_fkey;
DROP TABLE events;
DROP TABLE event_series;
DROP FUNCTION is_valid_route(jsonb);
ALTER TABLE tayfas DROP COLUMN members_can_create_events;
