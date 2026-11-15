-- "Boşum": arkadaşlara görünen boş vakit (issue #207, ADR-0028; platform
-- sahibi 2026-11-15, Notion "Faz 2 (v2)" §3).
--
-- Kişi başı gün başına tek satır. day bir takvim günüdür (saat değil);
-- "bugün" Europe/Istanbul tarihidir. slots günün kaba bölümleridir:
-- morning / afternoon / evening (birlikte olabilir) ya da yalnızca all_day.
-- note isteğe bağlı kısa nottur (kullanıcı içeriği; log'a ve olaya girmez).
--
-- Yalnızca kişinin kendisi ve kabul edilmiş, engel olmayan arkadaşları
-- okur (core'da her istekte). Bildirim ve olay yoktur. Geçmiş günler
-- okumada süzülür ve saklama işiyle silinir; hesap silmede hemen silinir.
-- Soft delete yoktur: içerik tablosu değil, kişinin kendi durum bilgisi.
--
-- Geri alma planı: Down tabloyu ve trigger fonksiyonunu kaldırır; boş
-- vakitler kaybolur (geçici veridir).

-- +goose Up
CREATE TABLE user_availability (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL,
  day        date NOT NULL,
  slots      text[] NOT NULL,
  note       text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_availability_user_day UNIQUE (user_id, day),
  CONSTRAINT user_availability_slots CHECK (
    array_ndims(slots) = 1
    AND cardinality(slots) BETWEEN 1 AND 3
    AND array_position(slots, NULL) IS NULL
    AND slots <@ ARRAY['morning', 'afternoon', 'evening', 'all_day']::text[]
    AND (NOT ('all_day' = ANY (slots)) OR cardinality(slots) = 1)
    AND (cardinality(slots) < 2 OR slots[1] <> slots[2])
    AND (cardinality(slots) < 3 OR (slots[1] <> slots[3] AND slots[2] <> slots[3]))
  ),
  CONSTRAINT user_availability_note CHECK (
    note IS NULL
    OR (char_length(note) BETWEEN 1 AND 60 AND note = btrim(note) AND note !~ '[[:cntrl:]]')
  )
);
-- Saklama işi geçmiş günleri siler.
CREATE INDEX user_availability_day_idx ON user_availability (day);
CREATE TRIGGER user_availability_updated_at BEFORE UPDATE ON user_availability
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- En fazla 30 gün ileri (Go'da da). Gün Europe/Istanbul takvimine göredir;
-- uygulama ile veritabanı saatinin gece yarısındaki farkı için iki yana bir
-- gün pay bırakılır, kesin aralığı Go uygular. day güncellenemediği için
-- (aşağıdaki kolon yetkileri) yalnızca eklemede denetlenir.
-- +goose StatementBegin
CREATE FUNCTION user_availability_day_window() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  today date := (now() AT TIME ZONE 'Europe/Istanbul')::date;
BEGIN
  IF NEW.day < today - 1 OR NEW.day > today + 31 THEN
    RAISE EXCEPTION 'user_availability: gün aralık dışında'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_availability_day_window BEFORE INSERT ON user_availability
  FOR EACH ROW EXECUTE FUNCTION user_availability_day_window();

-- core_app (ADR-0015): günün dilimlerini ve notunu değiştirir (ON CONFLICT),
-- satırı siler. user_id ve day değiştirilemez. Varsayılan yetki (00016)
-- SELECT ve INSERT'tir.
GRANT UPDATE (slots, note, updated_at), DELETE ON user_availability TO core_app;

-- +goose Down
DROP TABLE user_availability;
DROP FUNCTION user_availability_day_window();
