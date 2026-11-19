-- Tayfa sözlüğü (issue #244; ADR-0037 §4).
--
--   tayfa_vocabulary            tayfanın kendi sözlüğü: bir ad alanındaki
--                               (fish_species, book …) değerler. Değer
--                               tayfanın içeriğidir; Tayfam içerik sağlamaz.
--                               `value` gösterilen biçim (1–60, kırpılmış,
--                               boşlukları tekleştirilmiş), `value_normalized`
--                               tekillik biçimi (Türkçe küçük harf; uygulama
--                               vocabulary.Normalize ile üretir). Tekillik:
--                               (tayfa_id, namespace, value_normalized).
--                               `merged_into` birleştirmenin hedefidir: aynı
--                               tayfa ve ad alanında, kendisi birleştirilmemiş
--                               başka bir değer (zincir yok; tetikleyici
--                               korur). Birleştirmeyi moderatör ve yönetici
--                               yapar ve geri alır (`merged_into` boşalır).
--                               `created_by` hesap silinince boşalır, değer
--                               kalır (tayfanın ortak verisi).
--   event_log_entry_vocabulary  günlük kaydının sözlüğe bağlı alanları: kayıt
--                               kimliği, alan anahtarı ve kayıt anında seçilen
--                               ya da eklenen sözlük değeri. Gösterilen değer
--                               birleştirme hedefine çözülür; bağ ilk seçilen
--                               değerde kalır, böylece birleştirme geri
--                               alınınca kayıt kendi değerine döner.
--                               Tetikleyici bağın etkinliğin tayfasının ve
--                               tür sürümündeki alanın ad alanının sözlüğüne
--                               ait olduğunu denetler (IDOR ve tür sürümü dışı
--                               yazma). Kayıt kalıcı silinince bağ da silinir.
--
-- VERİ TAŞIYAN MIGRATION (hassas alan): `event_log_entries.field_values`
-- içinde sözlüğe bağlı alanların (tanımda `vocabulary` olan metin alanları;
-- bugün yalnızca balıkçılığın `catches.species` alanı) metin değerleri
-- sözlüğe taşınır: her (tayfa, ad alanı, normalize değer) için bir sözlük
-- satırı (ilk kaydın yazımı, ekleyeni ve zamanıyla), her kayıt için bir bağ;
-- değer `field_values`'tan çıkarılır. Silinmiş kayıtlar da taşınır.
--
-- Geri alma planı: Down bağlı değerleri (kaydın seçtiği değerin yazımıyla,
-- birleştirme hedefiyle değil) `field_values`'a metin olarak geri yazar ve
-- iki tabloyu kaldırır. Birleştirmeler kaybolur. Uygulama kodu da birlikte
-- geri alınmalıdır.

-- +goose Up

CREATE TABLE tayfa_vocabulary (
  id               uuid PRIMARY KEY,
  tayfa_id         uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  namespace        text NOT NULL,
  value            text NOT NULL,
  value_normalized text NOT NULL,
  merged_into      uuid,
  -- Değeri ilk ekleyen; hesap silinince NULL.
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tayfa_vocabulary_namespace CHECK (namespace ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT tayfa_vocabulary_value CHECK (
    char_length(value) BETWEEN 1 AND 60 AND value = btrim(value) AND value !~ '[[:cntrl:]]'),
  CONSTRAINT tayfa_vocabulary_value_normalized CHECK (
    char_length(value_normalized) BETWEEN 1 AND 120 AND value_normalized !~ '[[:cntrl:]]'),
  CONSTRAINT tayfa_vocabulary_not_self CHECK (merged_into IS DISTINCT FROM id),
  CONSTRAINT tayfa_vocabulary_key UNIQUE (tayfa_id, namespace, value_normalized),
  -- Birleştirme hedefi için: aynı tayfa ve ad alanı.
  CONSTRAINT tayfa_vocabulary_scope_key UNIQUE (id, tayfa_id, namespace),
  CONSTRAINT tayfa_vocabulary_merged_into_fkey FOREIGN KEY (merged_into, tayfa_id, namespace)
    REFERENCES tayfa_vocabulary (id, tayfa_id, namespace)
);
CREATE INDEX tayfa_vocabulary_merged_into_idx ON tayfa_vocabulary (merged_into) WHERE merged_into IS NOT NULL;
CREATE INDEX tayfa_vocabulary_created_by_idx ON tayfa_vocabulary (created_by) WHERE created_by IS NOT NULL;

-- Birleştirme düz kalır: hedef kendisi birleştirilmiş olamaz ve
-- birleştirilen değere bağlı başka değer kalamaz (uygulama önce onları
-- hedefe taşır).
-- +goose StatementBegin
CREATE FUNCTION tayfa_vocabulary_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.merged_into IS NULL THEN
    RETURN NEW;
  END IF;
  IF EXISTS (SELECT 1 FROM tayfa_vocabulary WHERE id = NEW.merged_into AND merged_into IS NOT NULL) THEN
    RAISE EXCEPTION 'tayfa_vocabulary: hedef birleştirilmiş'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'tayfa_vocabulary_guard';
  END IF;
  IF EXISTS (SELECT 1 FROM tayfa_vocabulary WHERE merged_into = NEW.id) THEN
    RAISE EXCEPTION 'tayfa_vocabulary: değere bağlı birleştirmeler var'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'tayfa_vocabulary_guard';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER tayfa_vocabulary_guard BEFORE INSERT OR UPDATE OF merged_into ON tayfa_vocabulary
  FOR EACH ROW EXECUTE FUNCTION tayfa_vocabulary_guard();

CREATE TABLE event_log_entry_vocabulary (
  entry_id      uuid NOT NULL REFERENCES event_log_entries (id) ON DELETE CASCADE,
  field_key     text NOT NULL,
  vocabulary_id uuid NOT NULL REFERENCES tayfa_vocabulary (id) ON DELETE CASCADE,

  PRIMARY KEY (entry_id, field_key),
  CONSTRAINT event_log_entry_vocabulary_field_key CHECK (field_key ~ '^[a-z][a-z0-9_]{1,39}$')
);
-- Birleştirmede etkilenen kayıtlar ve istatistik.
CREATE INDEX event_log_entry_vocabulary_vocabulary_idx ON event_log_entry_vocabulary (vocabulary_id);

-- Bağ, kaydın etkinliğinin tayfasının sözlüğüne ve etkinliğin tür
-- sürümünde bu alanın `vocabulary.namespace`'ine ait olmalıdır.
-- +goose StatementBegin
CREATE FUNCTION event_log_entry_vocabulary_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  fields jsonb;
  ns     text;
  tf     uuid;
BEGIN
  SELECT event_component_definition(l.event_id, l.component_key) -> 'params' -> 'fields', e.tayfa_id
    INTO fields, tf
  FROM event_log_entries l
  JOIN events e ON e.id = l.event_id
  WHERE l.id = NEW.entry_id;
  IF jsonb_typeof(fields) = 'array' THEN
    SELECT f -> 'vocabulary' ->> 'namespace' INTO ns
    FROM jsonb_array_elements(fields) AS f
    WHERE f ->> 'key' = NEW.field_key
    LIMIT 1;
  END IF;
  IF ns IS NULL OR NOT EXISTS (
    SELECT 1 FROM tayfa_vocabulary v WHERE v.id = NEW.vocabulary_id AND v.tayfa_id = tf AND v.namespace = ns
  ) THEN
    RAISE EXCEPTION 'event_log_entry_vocabulary: alan ya da sözlük değeri uyuşmuyor'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_log_entry_vocabulary_guard';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_log_entry_vocabulary_guard BEFORE INSERT OR UPDATE ON event_log_entry_vocabulary
  FOR EACH ROW EXECUTE FUNCTION event_log_entry_vocabulary_guard();

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016). Sözlük
-- değeri silinmez ve yazımı değişmez; yalnızca birleştirme ve hesap
-- silmede ekleyen değişir. Bağ değişmez; kayıtla birlikte (cascade) silinir.
GRANT SELECT, INSERT ON tayfa_vocabulary TO core_app;
GRANT UPDATE (merged_into, created_by) ON tayfa_vocabulary TO core_app;
GRANT SELECT, INSERT ON event_log_entry_vocabulary TO core_app;

-- Mevcut günlük değerleri sözlüğe. Normalize biçim uygulamanınkiyle aynıdır
-- (Türkçe büyük harfler açıkça çevrilir, sonra lower; boşluklar tekleşir).
-- Yardımcı fonksiyon migration sonunda kalkar.
-- +goose StatementBegin
CREATE FUNCTION vocabulary_move_rows()
RETURNS TABLE (entry_id uuid, user_id uuid, created_at timestamptz, tayfa_id uuid, field_key text, namespace text,
  value text, value_normalized text)
LANGUAGE sql STABLE AS $$
  SELECT x.entry_id, x.user_id, x.created_at, x.tayfa_id, x.field_key, x.namespace, x.value,
    lower(translate(x.value, 'IİÇĞÖŞÜ', 'ıiçğöşü'))
  FROM (
    SELECT l.id AS entry_id, l.user_id, l.created_at, e.tayfa_id, f ->> 'key' AS field_key,
      f -> 'vocabulary' ->> 'namespace' AS namespace,
      left(btrim(regexp_replace(l.field_values ->> (f ->> 'key'), '\s+', ' ', 'g')), 60) AS value
    FROM event_log_entries l
    JOIN events e ON e.id = l.event_id
    CROSS JOIN LATERAL jsonb_array_elements(
      CASE WHEN jsonb_typeof(event_component_definition(l.event_id, l.component_key) -> 'params' -> 'fields') = 'array'
        THEN event_component_definition(l.event_id, l.component_key) -> 'params' -> 'fields' ELSE '[]'::jsonb END
    ) AS f
    WHERE f -> 'vocabulary' ->> 'namespace' IS NOT NULL
      AND jsonb_typeof(l.field_values -> (f ->> 'key')) = 'string'
  ) AS x
  -- left() boşlukla bitebilir; boş ya da kırpılmamış değer taşınmaz (alandan
  -- yine çıkarılır).
  WHERE x.value <> '' AND x.value = btrim(x.value)
$$;
-- +goose StatementEnd

INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized, created_by, created_at)
SELECT DISTINCT ON (m.tayfa_id, m.namespace, m.value_normalized)
  uuidv7(), m.tayfa_id, m.namespace, m.value, m.value_normalized, m.user_id, m.created_at
FROM vocabulary_move_rows() AS m
ORDER BY m.tayfa_id, m.namespace, m.value_normalized, m.created_at, m.entry_id;

INSERT INTO event_log_entry_vocabulary (entry_id, field_key, vocabulary_id)
SELECT m.entry_id, m.field_key, v.id
FROM vocabulary_move_rows() AS m
JOIN tayfa_vocabulary v
  ON v.tayfa_id = m.tayfa_id AND v.namespace = m.namespace AND v.value_normalized = m.value_normalized;

DROP FUNCTION vocabulary_move_rows();

UPDATE event_log_entries AS l SET field_values = l.field_values - k.keys
FROM (
  SELECT l2.id, array_agg(f ->> 'key') AS keys
  FROM event_log_entries l2
  CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(event_component_definition(l2.event_id, l2.component_key) -> 'params' -> 'fields') = 'array'
      THEN event_component_definition(l2.event_id, l2.component_key) -> 'params' -> 'fields' ELSE '[]'::jsonb END
  ) AS f
  WHERE f -> 'vocabulary' ->> 'namespace' IS NOT NULL AND l2.field_values ? (f ->> 'key')
  GROUP BY l2.id
) AS k
WHERE l.id = k.id;

-- +goose Down

UPDATE event_log_entries AS l SET field_values = l.field_values || b.vals
FROM (
  SELECT x.entry_id, jsonb_object_agg(x.field_key, v.value) AS vals
  FROM event_log_entry_vocabulary x
  JOIN tayfa_vocabulary v ON v.id = x.vocabulary_id
  GROUP BY x.entry_id
) AS b
WHERE l.id = b.entry_id;

DROP TABLE event_log_entry_vocabulary;
DROP FUNCTION event_log_entry_vocabulary_guard();
DROP TABLE tayfa_vocabulary;
DROP FUNCTION tayfa_vocabulary_guard();
