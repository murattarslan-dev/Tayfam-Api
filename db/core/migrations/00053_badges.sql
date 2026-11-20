-- Rozetler (issue #258; ADR-0039; Notion "Faz 3 — Katalog ayrıntıları ve
-- şema ekleri (2026-11-18)" §E.2).
--
--   catalog_badges    Rozet tanımları: katalog gibi veridir. key değişmez;
--                     scope (user / tayfa), labels, icon (istemcinin sabit
--                     ikon setinden), rule (kapalı şema, Go'da
--                     catalog.ParseBadgeRule / catalog.CheckBadge),
--                     status (active / retired). GET /v1/catalog yanıtında
--                     badges olarak döner; kazanılmış rozetin adı ve ikonu
--                     buradan okunur, bu yüzden emekli rozetler de döner.
--   user_badges       Kişinin kazandığı rozetler. Tekillik: rozet + kişi +
--                     dönem (+ şampiyonlukta etkinlik). Şampiyonlukta
--                     tayfa_id ve event_id doludur, diğerlerinde boştur.
--   tayfa_badges      Tayfanın kazandığı rozetler. Tekillik: rozet + tayfa +
--                     dönem.
--   event_badge_runs  Şampiyonluk rozetleri verilmiş (kutlaması donmuş)
--                     etkinlikler: arka plan işi her etkinliğe bir kez
--                     bakar.
--
-- Kurallar (ADR-0039 §2):
--   * Rozet geri alınmaz: core_app user_badges'i yalnızca hesap silmede
--     siler, tayfa_badges'i hiç silemez; hiçbirini değiştiremez. Etkinlik
--     ya da tayfa sonradan silinse de kişinin şampiyonluk rozeti kalır
--     (event_id ve tayfa_id bu yüzden foreign key değildir).
--   * Kazanılmış rozetin tanımı silinemez (foreign key); emekliye ayrılır.
--   * Tanımın key'i, scope'u, kural türü ve dönemi değişmez: verilmiş
--     rozetlerin anlamı (ve dönem anahtarı) değişirdi.
--   * Kişi rozeti kişi tanımına, tayfa rozeti tayfa tanımına bağlanır;
--     şampiyonluk rozeti etkinlikle, yıllık eşik yılla (YYYY), diğerleri
--     'all' dönemiyle yazılır. Tetikleyici bunu ikinci savunma hattı olarak
--     denetler (ADR-0011); uygulama zaten böyle yazar.
--   * Hassas ölçümler ve sensitive_attendance türleri hiçbir kuralda yer
--     alamaz: katalog doğrulaması (catalog.CheckBadge) reddeder.
--
-- Tanım listesi bu migration'da yoktur: platform sahibinin onaylayacağı
-- Notion taslağından "Tam katalog verisi" işiyle (#260) gelir; tanımları
-- platform paneli (#262) yönetir. Tablo boş olduğu için katalog değişmez,
-- catalog_version artmaz. Rozet tanımı her değiştiğinde catalog_version
-- artar (değiştiren migration ya da platform paneli artırır).
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down tabloları, tetikleyicileri ve fonksiyonları
-- kaldırır; kazanılmış rozetler de gider (geri alma yalnızca rozet
-- verilmeden önce yapılmalıdır). Uygulama kodu da birlikte geri
-- alınmalıdır.

-- +goose Up

CREATE TABLE catalog_badges (
  key        text PRIMARY KEY,
  scope      text NOT NULL,
  -- Listeleme sırası.
  position   integer NOT NULL,
  -- Dil → metin; Türkçe zorunlu.
  labels     jsonb NOT NULL,
  icon       text NOT NULL,
  rule       jsonb NOT NULL,
  status     text NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT catalog_badges_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_badges_scope CHECK (scope IN ('user', 'tayfa')),
  CONSTRAINT catalog_badges_position CHECK (position BETWEEN 0 AND 9999),
  CONSTRAINT catalog_badges_labels CHECK (
    jsonb_typeof(labels) = 'object' AND COALESCE(jsonb_typeof(labels -> 'tr') = 'string', false)),
  CONSTRAINT catalog_badges_icon CHECK (icon ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_badges_rule CHECK (
    jsonb_typeof(rule) = 'object' AND octet_length(rule::text) <= 512
    AND COALESCE(rule ->> 'kind' IN ('threshold', 'first', 'champion'), false)),
  -- İlk katılım ve şampiyonluk kişiye verilir.
  CONSTRAINT catalog_badges_rule_scope CHECK (rule ->> 'kind' = 'threshold' OR scope = 'user'),
  CONSTRAINT catalog_badges_status CHECK (status IN ('active', 'retired'))
);
CREATE TRIGGER catalog_badges_updated_at BEFORE UPDATE ON catalog_badges
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Anahtar, kapsam, kural türü ve dönem değişmez.
-- +goose StatementBegin
CREATE FUNCTION catalog_badges_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.key <> OLD.key OR NEW.scope <> OLD.scope
     OR (NEW.rule ->> 'kind') IS DISTINCT FROM (OLD.rule ->> 'kind')
     OR (NEW.rule ->> 'period') IS DISTINCT FROM (OLD.rule ->> 'period') THEN
    RAISE EXCEPTION 'rozetin anahtarı, kapsamı, kural türü ve dönemi değişmez'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_badges_guard BEFORE UPDATE ON catalog_badges
  FOR EACH ROW EXECUTE FUNCTION catalog_badges_guard();

CREATE TABLE user_badges (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL,
  badge_key  text NOT NULL REFERENCES catalog_badges (key),
  -- 'all' ya da yıllık eşikte yıl (YYYY).
  period_key text NOT NULL,
  -- Şampiyonlukta etkinliğin tayfası ve etkinlik; diğerlerinde NULL.
  tayfa_id   uuid,
  event_id   uuid,
  earned_at  timestamptz NOT NULL,

  CONSTRAINT user_badges_period CHECK (period_key = 'all' OR period_key ~ '^[0-9]{4}$'),
  CONSTRAINT user_badges_event CHECK ((tayfa_id IS NULL) = (event_id IS NULL)),
  CONSTRAINT user_badges_once UNIQUE NULLS NOT DISTINCT (user_id, badge_key, period_key, event_id)
);
CREATE INDEX user_badges_tayfa_idx ON user_badges (tayfa_id, earned_at) WHERE tayfa_id IS NOT NULL;

CREATE TABLE tayfa_badges (
  id         uuid PRIMARY KEY,
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  badge_key  text NOT NULL REFERENCES catalog_badges (key),
  period_key text NOT NULL,
  earned_at  timestamptz NOT NULL,

  CONSTRAINT tayfa_badges_period CHECK (period_key = 'all' OR period_key ~ '^[0-9]{4}$'),
  CONSTRAINT tayfa_badges_once UNIQUE (tayfa_id, badge_key, period_key)
);

-- Kazanılan rozet kendi kapsamındaki tanıma bağlanır; şampiyonluk
-- etkinlikle, yıllık eşik yılla, diğerleri 'all' ile yazılır.
-- +goose StatementBegin
CREATE FUNCTION earned_badge_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  b catalog_badges%ROWTYPE;
  champion boolean;
  yearly boolean;
BEGIN
  SELECT * INTO b FROM catalog_badges WHERE key = NEW.badge_key;
  IF NOT FOUND OR b.scope <> TG_ARGV[0] THEN
    RAISE EXCEPTION 'rozet bu kapsamda değil' USING ERRCODE = 'check_violation';
  END IF;
  champion := (b.rule ->> 'kind') = 'champion';
  yearly := (b.rule ->> 'kind') = 'threshold' AND (b.rule ->> 'period') = 'year';
  IF yearly <> (NEW.period_key <> 'all') THEN
    RAISE EXCEPTION 'rozetin dönemi kuralına uymuyor' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_ARGV[0] = 'user' AND champion <> (NEW.event_id IS NOT NULL) THEN
    RAISE EXCEPTION 'yalnızca şampiyonluk rozeti bir etkinliğe bağlıdır' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER user_badges_guard BEFORE INSERT ON user_badges
  FOR EACH ROW EXECUTE FUNCTION earned_badge_guard('user');
CREATE TRIGGER tayfa_badges_guard BEFORE INSERT ON tayfa_badges
  FOR EACH ROW EXECUTE FUNCTION earned_badge_guard('tayfa');

CREATE TABLE event_badge_runs (
  event_id uuid PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
  done_at  timestamptz NOT NULL
);

-- core_app tanımları yalnızca okur (varsayılan yetki SELECT ve INSERT'tir,
-- 00016); tanımları migration (ileride platform paneli) yazar. Kazanılmış
-- rozetler eklenir, değişmez; kişininkiler yalnızca hesap silmede silinir.
REVOKE INSERT ON catalog_badges FROM core_app;
GRANT SELECT ON catalog_badges TO core_app;
GRANT SELECT, INSERT, DELETE ON user_badges TO core_app;
GRANT SELECT, INSERT ON tayfa_badges TO core_app;
GRANT SELECT, INSERT ON event_badge_runs TO core_app;

-- +goose Down

DROP TABLE event_badge_runs;
DROP TABLE tayfa_badges;
DROP TABLE user_badges;
DROP FUNCTION earned_badge_guard();
DROP TABLE catalog_badges;
DROP FUNCTION catalog_badges_guard();
