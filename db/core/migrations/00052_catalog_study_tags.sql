-- Ders ve konu etiket listesi (issue #259; ADR-0040 §5; Notion "Faz 3 —
-- Katalog ayrıntıları ve şema ekleri (2026-11-18)" E.4).
--
--   catalog_study_tags  Eğitim modüllerinin (materyal kütüphanesi, soru
--                       havuzu) ders ve konu etiketleri: sınav/düzey → ders
--                       → konu, en fazla üç seviye. Katalog verisidir;
--                       GET /v1/catalog yanıtında study_tags olarak döner.
--
-- Kurallar:
--   * key değişmez ve bütün listede tekildir; içerik etiketi bu anahtarla
--     tutar. parent_key de değişmez (etiketi taşımak eski içeriğin anlamını
--     değiştirirdi). İkisini de tetikleyici korur.
--   * Kaldırılan etiket silinmez, status = 'retired' olur: yeni içerikte
--     seçilemez, eski içerik göstermeye devam eder.
--   * Her düzeyin ve dersin altında "Diğer" vardır; anahtarı
--     <üst anahtar>_other'dır. Bu bir içerik kuralıdır: Go tarafında
--     catalog.CheckStudyTags denetler (seed testi ve platform paneli).
--   * Yayınevi ya da marka adı etiket olamaz (platform sahibinin içerik
--     kuralı).
--   * Liste her değiştiğinde catalog_meta.catalog_version artar (ADR-0040
--     §5); değiştiren migration ya da platform paneli artırır.
--
-- Liste içeriği bu migration'da yoktur: platform sahibinin onaylayacağı
-- Notion taslağından "Tam katalog verisi" işiyle (#260) gelir. Tablo boş
-- olduğu için katalog değişmez, catalog_version artmaz.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down tabloyu ve tetikleyiciyi kaldırır (tablo boştur;
-- #260 sonrasında geri almak seed'i de kaldırır). Uygulama kodu da birlikte
-- geri alınmalıdır.

-- +goose Up

CREATE TABLE catalog_study_tags (
  key        text PRIMARY KEY,
  -- NULL: sınav/düzey. Düzey → ders → konu.
  parent_key text REFERENCES catalog_study_tags (key),
  -- Aynı üst etiketin altındaki sıra.
  position   integer NOT NULL,
  -- Dil → metin; Türkçe zorunlu.
  labels     jsonb NOT NULL,
  status     text NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT catalog_study_tags_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_study_tags_parent CHECK (parent_key IS NULL OR parent_key <> key),
  CONSTRAINT catalog_study_tags_position CHECK (position BETWEEN 0 AND 9999),
  CONSTRAINT catalog_study_tags_labels CHECK (
    jsonb_typeof(labels) = 'object' AND COALESCE(jsonb_typeof(labels -> 'tr') = 'string', false)),
  CONSTRAINT catalog_study_tags_status CHECK (status IN ('active', 'retired'))
);
CREATE INDEX catalog_study_tags_parent_key ON catalog_study_tags (parent_key);
CREATE TRIGGER catalog_study_tags_updated_at BEFORE UPDATE ON catalog_study_tags
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Anahtar ve üst etiket değişmez; en fazla üç seviye. Üst etiket
-- değişemediği için döngü oluşamaz.
-- +goose StatementBegin
CREATE FUNCTION catalog_study_tags_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  depth integer := 1;
  p     text := NEW.parent_key;
BEGIN
  IF TG_OP = 'UPDATE' AND (NEW.key <> OLD.key OR NEW.parent_key IS DISTINCT FROM OLD.parent_key) THEN
    RAISE EXCEPTION 'ders/konu etiketinin anahtarı ve üst etiketi değişmez'
      USING ERRCODE = 'check_violation';
  END IF;
  WHILE p IS NOT NULL LOOP
    depth := depth + 1;
    IF depth > 3 THEN
      RAISE EXCEPTION 'ders/konu etiketi en fazla üç seviyedir'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT t.parent_key INTO p FROM catalog_study_tags t WHERE t.key = p;
  END LOOP;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_study_tags_guard BEFORE INSERT OR UPDATE ON catalog_study_tags
  FOR EACH ROW EXECUTE FUNCTION catalog_study_tags_guard();

-- core_app yalnızca okur (varsayılan yetki SELECT ve INSERT'tir, 00016).
-- Liste migration'la (ileride platform paneliyle) değişir.
REVOKE INSERT ON catalog_study_tags FROM core_app;
GRANT SELECT ON catalog_study_tags TO core_app;

-- +goose Down

DROP TABLE catalog_study_tags;
DROP FUNCTION catalog_study_tags_guard();
