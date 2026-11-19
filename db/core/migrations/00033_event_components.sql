-- Bileşen çerçevesi: etkinliğin bileşen seçimleri, hakemler ve bileşen
-- tablolarının ortak koruması (issue #238; ADR-0025 §4–§6, ADR-0037 §2).
--
--   event_components   organizatörün etkinlik için yaptığı seçimler:
--                      `optional` bileşenin açık/kapalı hali ve
--                      `organizer_choice` parametrelerinin seçilen değerleri.
--                      Satırı olmayan bileşen tanımdaki varsayılanla çalışır
--                      (optional.default, parametrenin default'u).
--   event_referees     organizatörün atadığı hakemler. Atama etkinliğin
--                      tamamı (component_key NULL), bir bileşeni ya da o
--                      bileşenin içindeki bir kapsam (masa, maç; scope_id)
--                      içindir. scope_id'nin gösterdiği satır bileşenin kendi
--                      tablosundadır; bileşen onu silerken atamayı da siler.
--
-- Fonksiyonlar:
--   event_component_definition(event, key)  bileşenin, etkinliğin tür
--                      sürümündeki tanımı (jsonb); bileşen yoksa NULL.
--   event_component_enabled(event, key)     bileşen açık mı; bileşen yoksa
--                      NULL.
--   event_component_guard('<kind>')         BİLEŞEN TABLOLARININ ORTAK
--                      TRIGGER'I (ADR-0025 §4: "bir bileşenin tablosuna,
--                      etkinliğin tür sürümünde o bileşen yoksa yazılamaz").
--                      (event_id, component_key) kolonlarını taşıyan her
--                      bileşen tablosu şunu ekler:
--
--                        CREATE TRIGGER <tablo>_component_guard
--                          BEFORE INSERT OR UPDATE OF event_id, component_key ON <tablo>
--                          FOR EACH ROW EXECUTE FUNCTION event_component_guard('<kind>');
--
--                      Satır; bileşen etkinliğin tür sürümünde yoksa, türü
--                      (kind) farklıysa ya da bileşen kapalıysa yazılamaz.
--
-- Uygulama aynı kuralları önce kendisi denetler (internal/core/app/component);
-- buradakiler ikinci savunma hattıdır. Seçimlerin etkinlik başlayınca
-- kilitlenmesi ve bitişten 7 gün sonraki donma yalnızca uygulamadadır (saat
-- uygulamanındır; "Şimdi başlat" etkinliği seçimleriyle birlikte, başlamış
-- olarak açılır).
--
-- Geri alma planı: Down iki tabloyu ve fonksiyonları kaldırır. Seçimler ve
-- hakem atamaları kaybolur (etkinlikler tanımdaki varsayılanlara döner);
-- başka hiçbir tablo değişmez. Bu migration veri taşımaz ve silmez.

-- +goose Up

CREATE TABLE event_components (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind).
  component_key text NOT NULL,
  enabled       boolean NOT NULL,
  -- Yalnızca organizatöre bırakılan parametreler: ad → seçilen değer.
  params        jsonb NOT NULL DEFAULT '{}',
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_components_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_components_params CHECK (
    jsonb_typeof(params) = 'object' AND octet_length(params::text) <= 2048)
);
CREATE TRIGGER event_components_updated_at BEFORE UPDATE ON event_components
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose StatementBegin
CREATE FUNCTION event_component_definition(p_event uuid, p_key text) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT c
  FROM events e
  JOIN catalog_event_type_versions v ON v.type_key = e.type AND v.version = e.type_version
  CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(v.definition -> 'components') = 'array' THEN v.definition -> 'components' ELSE '[]'::jsonb END
  ) AS c
  WHERE e.id = p_event AND COALESCE(c ->> 'key', c ->> 'kind') = p_key
  LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION event_component_enabled(p_event uuid, p_key text) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT COALESCE(
    (SELECT ec.enabled FROM event_components ec WHERE ec.event_id = p_event AND ec.component_key = p_key),
    (d.def -> 'optional' ->> 'default')::boolean,
    true)
  FROM (SELECT event_component_definition(p_event, p_key) AS def) AS d
  WHERE d.def IS NOT NULL
$$;
-- +goose StatementEnd

-- Seçim, etkinliğin tür sürümündeki bir bileşene aittir; `optional` olmayan
-- bileşen kapatılamaz; parametre organizatöre bırakılmış olmalı ve değer
-- tanımdaki seçeneklerden biri olmalıdır.
-- +goose StatementBegin
CREATE FUNCTION event_components_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  def jsonb;
BEGIN
  def := event_component_definition(NEW.event_id, NEW.component_key);
  IF def IS NULL THEN
    RAISE EXCEPTION 'event_components: bileşen etkinliğin tür sürümünde yok'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_components_guard';
  END IF;
  IF NOT NEW.enabled AND jsonb_typeof(def -> 'optional') IS DISTINCT FROM 'object' THEN
    RAISE EXCEPTION 'event_components: bileşen isteğe bağlı değil'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_components_guard';
  END IF;
  -- Nesne olmayan params'ı event_components_params CHECK'i reddeder
  -- (CHECK, BEFORE trigger'dan sonra çalışır).
  IF jsonb_typeof(NEW.params) IS DISTINCT FROM 'object' THEN
    RETURN NEW;
  END IF;
  IF EXISTS (
    SELECT 1 FROM jsonb_each(NEW.params) AS p
    WHERE NOT COALESCE((def -> 'params' -> p.key -> 'organizer_choice') @> jsonb_build_array(p.value), false)
  ) THEN
    RAISE EXCEPTION 'event_components: parametre seçilemez ya da değer seçenekler arasında değil'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_components_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_components_guard BEFORE INSERT OR UPDATE ON event_components
  FOR EACH ROW EXECUTE FUNCTION event_components_guard();

-- Bileşen tablolarının ortak trigger'ı; TG_ARGV[0] bileşenin türüdür (kind).
-- +goose StatementBegin
CREATE FUNCTION event_component_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  def jsonb;
BEGIN
  IF TG_NARGS <> 1 THEN
    RAISE EXCEPTION 'event_component_guard: bileşen türü verilmemiş';
  END IF;
  def := event_component_definition(NEW.event_id, NEW.component_key);
  IF def IS NULL OR def ->> 'kind' IS DISTINCT FROM TG_ARGV[0] THEN
    RAISE EXCEPTION 'bileşen etkinliğin tür sürümünde yok'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_component_guard';
  END IF;
  IF NOT event_component_enabled(NEW.event_id, NEW.component_key) THEN
    RAISE EXCEPTION 'bileşen kapalı'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_component_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE event_referees (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Hakem (tayfanın etkin üyesi; uygulama denetler). Hesap silinince satır
  -- silinir.
  user_id       uuid NOT NULL,
  -- NULL: etkinliğin bütün bileşenleri.
  component_key text,
  -- Bileşenin içindeki kapsam (masa, maç); NULL: bileşenin tamamı.
  scope_id      uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_referees_key CHECK (component_key IS NULL OR component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_referees_scope CHECK (scope_id IS NULL OR component_key IS NOT NULL)
);
-- Aynı atama bir kez yazılır.
CREATE UNIQUE INDEX event_referees_assignment_key ON event_referees (
  event_id, user_id, COALESCE(component_key, ''), COALESCE(scope_id, '00000000-0000-0000-0000-000000000000'::uuid));
-- Hesap silme.
CREATE INDEX event_referees_user_idx ON event_referees (user_id);

-- +goose StatementBegin
CREATE FUNCTION event_referees_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.component_key IS NOT NULL AND event_component_definition(NEW.event_id, NEW.component_key) IS NULL THEN
    RAISE EXCEPTION 'event_referees: bileşen etkinliğin tür sürümünde yok'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_referees_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_referees_guard BEFORE INSERT OR UPDATE ON event_referees
  FOR EACH ROW EXECUTE FUNCTION event_referees_guard();

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016):
-- seçimin değeri değişir (ON CONFLICT), satır başka etkinliğe ya da bileşene
-- taşınamaz ve silinmez (etkinlikle gider). Hakem ataması değişmez; kaldırılır.
GRANT UPDATE (enabled, params, updated_at) ON event_components TO core_app;
GRANT DELETE ON event_referees TO core_app;

-- +goose Down
DROP TABLE event_referees;
DROP FUNCTION event_referees_guard();
DROP FUNCTION event_component_guard();
DROP TABLE event_components;
DROP FUNCTION event_components_guard();
DROP FUNCTION event_component_enabled(uuid, text);
DROP FUNCTION event_component_definition(uuid, text);
