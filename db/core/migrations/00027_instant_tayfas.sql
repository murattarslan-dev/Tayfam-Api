-- Anlık (bireysel) etkinlik (issue #209, ADR-0032; platform sahibi
-- 2026-11-15, Notion "Faz 2 (v2)" §5).
--
-- Anlık etkinlik, arka planda gizli ve tek etkinlikli bir tayfadır:
-- tayfas.kind = 'instant'. Organizatör tek yöneticidir; davetliler üyedir.
-- "Bu ekiple tayfa kur" tayfayı kalıcıya çevirir: kind = 'standard',
-- converted_at dolar ve bir daha değişmez.
--
-- Veritabanı türün değişmezlerini ayrıca korur (ADR-0011):
--   - anlık tayfa Keşfet'e çıkamaz (visibility = 'private') ve katılım her
--     zaman onaylıdır (join_policy = 'approval');
--   - tür yalnızca instant → standard yönünde, converted_at ile birlikte
--     değişir (tayfas_kind_guard);
--   - anlık tayfada en fazla bir etkinlik olur (events_instant_single).
-- Anlık tayfanın adı etkinliğin başlığıdır (1–120); kalıcı tayfada ad
-- sınırı değişmedi (3–50).
--
-- Geri alma planı: Down trigger'ları, CHECK'leri, indeksi ve kolonları
-- kaldırır, ad sınırını eski haline getirir. Down'dan önce anlık tayfalar
-- silinmiş ya da dönüştürülmüş olmalıdır (ad sınırı 3–50'ye döner; 3'ten
-- kısa ya da 50'den uzun adlı anlık tayfa varken Down başarısız olur).

-- +goose Up
ALTER TABLE tayfas
  ADD COLUMN kind text NOT NULL DEFAULT 'standard',
  ADD COLUMN converted_at timestamptz,
  ADD CONSTRAINT tayfas_kind CHECK (kind IN ('standard', 'instant')),
  ADD CONSTRAINT tayfas_converted_at CHECK (converted_at IS NULL OR kind = 'standard'),
  -- Anlık tayfa hiçbir zaman listelenmez ve kendiliğinden üye almaz.
  ADD CONSTRAINT tayfas_instant_private CHECK (kind <> 'instant' OR (visibility = 'private' AND join_policy = 'approval'));

ALTER TABLE tayfas DROP CONSTRAINT tayfas_name_len;
ALTER TABLE tayfas DROP CONSTRAINT tayfas_name_normalized_len;
ALTER TABLE tayfas
  ADD CONSTRAINT tayfas_name_len CHECK (
    (kind = 'standard' AND char_length(name) BETWEEN 3 AND 50) OR
    (kind = 'instant' AND char_length(name) BETWEEN 1 AND 120)
  ),
  ADD CONSTRAINT tayfas_name_normalized_len CHECK (
    (kind = 'standard' AND char_length(name_normalized) BETWEEN 3 AND 50) OR
    (kind = 'instant' AND char_length(name_normalized) BETWEEN 1 AND 120)
  );

-- Kullanıcının açık anlık etkinlik sayımı (kişi başı en fazla 3).
CREATE INDEX tayfas_instant_created_by_idx ON tayfas (created_by)
  WHERE kind = 'instant' AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION tayfas_kind_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.converted_at IS NOT NULL THEN
      RAISE EXCEPTION 'yeni tayfa dönüştürülmüş olamaz' USING ERRCODE = 'TF120';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.converted_at IS NOT NULL AND NEW.converted_at IS DISTINCT FROM OLD.converted_at THEN
    RAISE EXCEPTION 'dönüşüm zamanı değişmez' USING ERRCODE = 'TF120';
  END IF;
  IF NEW.kind IS DISTINCT FROM OLD.kind THEN
    IF NOT (OLD.kind = 'instant' AND NEW.kind = 'standard' AND NEW.converted_at IS NOT NULL) THEN
      RAISE EXCEPTION 'tayfa türü yalnızca anlıktan kalıcıya değişir' USING ERRCODE = 'TF120';
    END IF;
  ELSIF NEW.converted_at IS DISTINCT FROM OLD.converted_at THEN
    RAISE EXCEPTION 'dönüşüm zamanı yalnızca türle birlikte yazılır' USING ERRCODE = 'TF120';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tayfas_kind_guard BEFORE INSERT OR UPDATE OF kind, converted_at ON tayfas
  FOR EACH ROW EXECUTE FUNCTION tayfas_kind_guard();

-- Anlık tayfada tek etkinlik; silinmiş etkinlik de sayılır. Yalnızca anlık
-- tayfanın satırı kilitlenir (aynı anda gelen iki ekleme sırayla görülür);
-- kalıcı tayfada kilit alınmaz, seri üretimiyle kilit sırası değişmez.
-- +goose StatementBegin
CREATE FUNCTION events_instant_single() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  k text;
BEGIN
  SELECT kind INTO k FROM tayfas WHERE id = NEW.tayfa_id;
  IF k IS DISTINCT FROM 'instant' THEN
    RETURN NEW;
  END IF;
  -- Tür yalnızca instant → standard değişir; kilitten sonra yeniden okunur.
  SELECT kind INTO k FROM tayfas WHERE id = NEW.tayfa_id FOR NO KEY UPDATE;
  IF k = 'instant' AND EXISTS (SELECT 1 FROM events WHERE tayfa_id = NEW.tayfa_id) THEN
    RAISE EXCEPTION 'anlık tayfada tek etkinlik olur' USING ERRCODE = 'TF120';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER events_instant_single BEFORE INSERT ON events
  FOR EACH ROW EXECUTE FUNCTION events_instant_single();

-- +goose Down
DROP TRIGGER events_instant_single ON events;
DROP FUNCTION events_instant_single();
DROP TRIGGER tayfas_kind_guard ON tayfas;
DROP FUNCTION tayfas_kind_guard();
DROP INDEX tayfas_instant_created_by_idx;
ALTER TABLE tayfas DROP CONSTRAINT tayfas_name_len;
ALTER TABLE tayfas DROP CONSTRAINT tayfas_name_normalized_len;
ALTER TABLE tayfas
  DROP CONSTRAINT tayfas_instant_private,
  DROP CONSTRAINT tayfas_converted_at,
  DROP CONSTRAINT tayfas_kind,
  DROP COLUMN converted_at,
  DROP COLUMN kind;
ALTER TABLE tayfas
  ADD CONSTRAINT tayfas_name_len CHECK (char_length(name) BETWEEN 3 AND 50),
  ADD CONSTRAINT tayfas_name_normalized_len CHECK (char_length(name_normalized) BETWEEN 3 AND 50);
