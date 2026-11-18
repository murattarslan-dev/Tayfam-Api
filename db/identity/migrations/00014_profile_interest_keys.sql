-- Profil ilgi alanları katalog ilgi alanı anahtarı tutar (issue #237;
-- ADR-0025 §10; ADR-0009 kopya modeli; ADR-0041 §3).
--
-- profile_details.interests bugüne kadar etkinlik türü anahtarı tutuyordu
-- (yedi türlük CHECK listesi). İlgi alanı artık core'daki kataloğun bir
-- kaydıdır; identity kataloğun sahibi değildir (ADR-0002), bu yüzden değerler
-- kataloğun buradaki salt okunur KOPYASINA karşı denetlenir:
--
--   catalog_copy       tek satır: kopyanın yansıttığı katalog sürümü
--                      (core'daki catalog_meta.catalog_version)
--   catalog_interests  ilgi alanı anahtarları ve durumu (active / retired).
--                      Yalnızca anahtar ve durum tutulur; ad, ikon, sıra
--                      istemciye core'un GET /v1/catalog ucundan gider.
--
-- Kopyanın ilk hali bu migration'la gelir: core 00030'daki altı ilgi alanı,
-- katalog sürümü 1. Kopyanın core'un "catalog.changed" olayıyla (yalnızca
-- sürüm numarası) güncellenmesi ADR-0025 §10'da karar olarak yazılıdır, ama
-- olayın identity'ye taşınması ve kopyanın içeriğinin nereden okunacağı
-- henüz kararlaştırılmadı (PR açıklamasına bakın). O zamana kadar core'da
-- ilgi alanı ekleyen ya da emekliye ayıran her migration, aynı değişikliği
-- burada da yapan bir identity migration'ıyla birlikte gelmelidir.
--
-- Veri taşıma (VERİ DEĞİŞTİREN migration): mevcut değerler türden ilgi
-- alanına eşlenir (core 00032 ile aynı eşleme):
--
--   convoy → automotive; camp, hike → nature; fishing → water;
--   sports → sports; gaming → games; general → general
--
-- Aynı ilgi alanına düşen iki tür tek değer olur. Kişinin hangi türü seçtiği
-- bilgisi bu eşlemeyle kaybolur (ilgi alanı türden geniştir).
--
-- Denetim: değerler Go'da kopyaya karşı denetlenir (profilefields);
-- buradaki trigger ikinci savunma hattıdır: kopyada olmayan anahtar
-- yazılamaz. Emekli ilgi alanı kopyada kaldığı için eski profiller onu
-- göstermeye devam eder.
--
-- Yetki (ADR-0015, en az yetki): identity_app kopyayı yalnızca OKUR.
--
-- Geri alma planı: Down değerleri türe geri çevirir (automotive → convoy,
-- nature → camp, water → fishing, sports → sports, games → gaming, general
-- → general; camp / hike ayrımı geri gelmez), kopya tablolarını ve trigger'ı
-- kaldırır, eski CHECK'i geri koyar. Altı Faz 1 ilgi alanı dışında bir değer
-- varsa geri alma CHECK'te durur. Uygulamadan önce veritabanı yedeği alınır
-- (docs/runbooks).

-- +goose Up

CREATE TABLE catalog_copy (
  id              boolean PRIMARY KEY DEFAULT true,
  catalog_version bigint NOT NULL,
  updated_at      timestamptz NOT NULL DEFAULT now(),

  -- Tek satır.
  CONSTRAINT catalog_copy_single CHECK (id),
  CONSTRAINT catalog_copy_version CHECK (catalog_version >= 1)
);

CREATE TABLE catalog_interests (
  key    text PRIMARY KEY,
  status text NOT NULL DEFAULT 'active',

  CONSTRAINT catalog_interests_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_interests_status CHECK (status IN ('active', 'retired'))
);

-- identity_app yalnızca okur (varsayılan yetki SELECT ve INSERT'tir, 00008).
REVOKE INSERT ON catalog_copy, catalog_interests FROM identity_app;

INSERT INTO catalog_copy (catalog_version) VALUES (1);
INSERT INTO catalog_interests (key) VALUES
  ('automotive'), ('nature'), ('water'), ('sports'), ('games'), ('general');

ALTER TABLE profile_details DROP CONSTRAINT profile_details_interests;

UPDATE profile_details p
SET interests = COALESCE((
  SELECT array_agg(DISTINCT m.interest_key ORDER BY m.interest_key)
  FROM unnest(p.interests) AS t (event_type)
  JOIN (VALUES
    ('convoy', 'automotive'), ('camp', 'nature'), ('hike', 'nature'), ('fishing', 'water'),
    ('sports', 'sports'), ('gaming', 'games'), ('general', 'general')
  ) AS m (event_type, interest_key) ON m.event_type = t.event_type
), '{}')
WHERE cardinality(p.interests) > 0;

-- Yapısal sınır; değerlerin kendisi aşağıdaki trigger'da.
ALTER TABLE profile_details ADD CONSTRAINT profile_details_interests CHECK (
  cardinality(interests) <= 20 AND array_position(interests, NULL) IS NULL);

-- İlgi alanı anahtarları katalog kopyasında olmalıdır.
-- +goose StatementBegin
CREATE FUNCTION profile_details_interests_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM unnest(NEW.interests) AS k
    WHERE NOT EXISTS (SELECT 1 FROM catalog_interests i WHERE i.key = k)
  ) THEN
    RAISE EXCEPTION 'profile_details: bilinmeyen ilgi alanı'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'profile_details_interests_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER profile_details_interests_guard BEFORE INSERT OR UPDATE OF interests ON profile_details
  FOR EACH ROW EXECUTE FUNCTION profile_details_interests_guard();

-- +goose Down

DROP TRIGGER profile_details_interests_guard ON profile_details;
DROP FUNCTION profile_details_interests_guard();
ALTER TABLE profile_details DROP CONSTRAINT profile_details_interests;

UPDATE profile_details p
SET interests = COALESCE((
  SELECT array_agg(DISTINCT COALESCE(m.event_type, t.interest_key) ORDER BY COALESCE(m.event_type, t.interest_key))
  FROM unnest(p.interests) AS t (interest_key)
  LEFT JOIN (VALUES
    ('automotive', 'convoy'), ('nature', 'camp'), ('water', 'fishing'),
    ('sports', 'sports'), ('games', 'gaming'), ('general', 'general')
  ) AS m (interest_key, event_type) ON m.interest_key = t.interest_key
), '{}')
WHERE cardinality(p.interests) > 0;

ALTER TABLE profile_details ADD CONSTRAINT profile_details_interests CHECK (
  interests <@ ARRAY['convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general']::text[]
  AND cardinality(interests) <= 7);

DROP TABLE catalog_interests;
DROP TABLE catalog_copy;
