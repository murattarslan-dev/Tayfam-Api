-- Yüklenen dosyalar (issue #135; ADR-0007, ADR-0020; veri modeli v14
-- "Dosyalar", "Yüklenen dosya takibi").
--
-- Dosyanın kendisi R2'dedir; burada yalnızca kaydı durur. r2_key tahmin
-- edilemez rastgele bir addır (crypto/rand, 16 bayt, hex): ham dosya
-- incoming/<r2_key>, işlenmiş dosyalar media/<r2_key>/preview ve
-- media/<r2_key>/full. Anahtar log'a, olaya ve yanıtlara girmez.
--
-- Durumlar: pending (imzalı PUT linki verildi) → uploaded (nesne var, işleme
-- kuyruğunda) → processing → ready | rejected. deleting: R2'deki nesneleri
-- silinecek, ardından satır silinir (sahipsiz temizlik, içerik silinmesi,
-- hesap silme). Notion'daki "attached" durumu yerine bağlanma ayrı
-- alanlardadır (attached_type, attached_id): hazır bir dosya bağlansa da
-- bağlanmasa da sunulabilir durumdadır (ADR-0020).
--
-- İşleme kuyruğu bu tablonun kendisidir: işçi uploaded/processing
-- satırlarını run_after sırasıyla FOR UPDATE SKIP LOCKED ile kiralar
-- (status = processing, run_after = kira sonu). Kira biterse satır yeniden
-- alınır; deneme sayısı attempts'tedir.

-- +goose Up
CREATE TABLE uploads (
  id                uuid PRIMARY KEY,
  -- Sahibi. Yalnızca hesap silmede, bağlı bir kapak görselinde NULL olur
  -- (kapak tayfanındır, kişisel değildir); diğer dosyalar silinir.
  user_id           uuid,
  -- Kapak ve av fotoğrafının ait olduğu tayfa; avatarda NULL.
  tayfa_id          uuid,
  purpose           text NOT NULL,
  status            text NOT NULL DEFAULT 'pending',
  -- İstemcinin bildirdiği tür ve ham boyut; imzalı PUT yalnızca bu tür ve
  -- bu boyutla çalışır. Gerçek tür işlemede ilk baytlardan okunur.
  content_type      text NOT NULL,
  size_bytes        bigint NOT NULL,
  r2_key            text NOT NULL,
  -- İşlenmiş dosyaların türü, toplam boyutu ve büyük boyutun ölçüleri.
  output_type       text,
  stored_bytes      bigint,
  width             integer,
  height            integer,
  reject_reason     text,
  attempts          integer NOT NULL DEFAULT 0,
  run_after         timestamptz,
  attached_type     text,
  attached_id       uuid,
  attached_at       timestamptz,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  uploaded_at       timestamptz,
  ready_at          timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover')),
  CONSTRAINT uploads_status CHECK (status IN ('pending', 'uploaded', 'processing', 'ready', 'rejected', 'deleting')),
  CONSTRAINT uploads_content_type CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp', 'image/heic')),
  -- Ham dosya en fazla 10 MiB (ADR-0007).
  CONSTRAINT uploads_size_bytes CHECK (size_bytes BETWEEN 1 AND 10485760),
  CONSTRAINT uploads_r2_key CHECK (r2_key ~ '^[0-9a-f]{32}$'),
  CONSTRAINT uploads_r2_key_key UNIQUE (r2_key),
  CONSTRAINT uploads_tayfa CHECK ((purpose = 'avatar') = (tayfa_id IS NULL)),
  CONSTRAINT uploads_owner CHECK (user_id IS NOT NULL OR (purpose = 'cover' AND attached_type IS NOT NULL)),
  CONSTRAINT uploads_output CHECK (
    (output_type IS NULL) = (stored_bytes IS NULL)
    AND (output_type IS NULL) = (width IS NULL)
    AND (output_type IS NULL) = (height IS NULL)
    AND (output_type IS NULL OR output_type IN ('image/jpeg', 'image/webp'))
    AND (stored_bytes IS NULL OR stored_bytes > 0)
    AND (width IS NULL OR (width BETWEEN 1 AND 1600 AND height BETWEEN 1 AND 1600))
  ),
  CONSTRAINT uploads_ready CHECK (status NOT IN ('ready') OR (output_type IS NOT NULL AND ready_at IS NOT NULL)),
  CONSTRAINT uploads_reject_reason CHECK (
    (reject_reason IS NULL OR reject_reason IN ('not_image', 'unsupported_type', 'too_large', 'corrupt', 'size_mismatch', 'failed'))
    AND (status <> 'rejected' OR reject_reason IS NOT NULL)
    AND (reject_reason IS NULL OR status IN ('rejected', 'deleting'))
  ),
  CONSTRAINT uploads_attempts CHECK (attempts BETWEEN 0 AND 100),
  CONSTRAINT uploads_queue CHECK (status NOT IN ('uploaded', 'processing') OR run_after IS NOT NULL),
  CONSTRAINT uploads_attached CHECK (
    (attached_type IS NULL) = (attached_id IS NULL)
    AND (attached_type IS NULL) = (attached_at IS NULL)
    AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
    AND (attached_type IS NULL
      OR (purpose = 'avatar' AND attached_type = 'user')
      OR (purpose = 'catch_photo' AND attached_type = 'fishing_catch')
      OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event')))
  ),
  CONSTRAINT uploads_client_request_key UNIQUE (user_id, client_request_id)
);
-- İşleme kuyruğu.
CREATE INDEX uploads_queue_idx ON uploads (run_after) WHERE status IN ('uploaded', 'processing');
-- Sıklık sınırı ve kota.
CREATE INDEX uploads_user_created_idx ON uploads (user_id, created_at);
-- Bağlı dosya araması (eski avatar, içerik silinmesi).
CREATE INDEX uploads_attached_idx ON uploads (attached_type, attached_id) WHERE attached_type IS NOT NULL;
-- Temizlik: süresi geçen pending ve rejected, bağlanmamış ready, deleting.
CREATE INDEX uploads_cleanup_idx ON uploads (status, updated_at) WHERE status <> 'ready' OR attached_type IS NULL;
CREATE TRIGGER uploads_updated_at BEFORE UPDATE ON uploads FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- İçeriğin dosyası (ADR-0009 "Veri modeline eklenenler"). Herkese açık kopya
-- (cover_public_key, paylaşılabilirlik) Faz 2'dedir, burada yoktur.
ALTER TABLE tayfas ADD COLUMN cover_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
ALTER TABLE events ADD COLUMN cover_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
ALTER TABLE fishing_catches ADD COLUMN photo_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX tayfas_cover_upload_id_key ON tayfas (cover_upload_id) WHERE cover_upload_id IS NOT NULL;
CREATE UNIQUE INDEX events_cover_upload_id_key ON events (cover_upload_id) WHERE cover_upload_id IS NOT NULL;
CREATE UNIQUE INDEX fishing_catches_photo_upload_id_key ON fishing_catches (photo_upload_id) WHERE photo_upload_id IS NOT NULL;

-- İçerik yalnızca kendisine bağlanmış, hazır ve doğru amaçlı dosyayı
-- gösterebilir (uygulama katmanının ikinci savunma hattı). Uygulama önce
-- dosyayı bağlar (uploads.attached_*), sonra içeriğin kolonunu yazar.
-- TG_ARGV: kolon adı, amaç, attached_type.
-- +goose StatementBegin
CREATE FUNCTION uploads_check_ref() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  new_ref uuid := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
  old_ref uuid;
BEGIN
  IF new_ref IS NULL THEN
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' THEN
    old_ref := (to_jsonb(OLD) ->> TG_ARGV[0])::uuid;
    IF old_ref IS NOT DISTINCT FROM new_ref THEN
      RETURN NEW;
    END IF;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM uploads
    WHERE id = new_ref AND status = 'ready' AND purpose = TG_ARGV[1]
      AND attached_type = TG_ARGV[2] AND attached_id = NEW.id
  ) THEN
    RAISE EXCEPTION 'dosya bu içeriğe bağlı değil' USING ERRCODE = 'TF080';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

-- İçerik dosyasını bıraktığında (kolon değişti ya da satır kalıcı silindi;
-- cascade dahil) eski dosya silme kuyruğuna girer: hangi kod yolu olursa
-- olsun içerik gidince dosyası da gider (ADR-0007 "Temizlik"). Soft delete
-- dosyayı bırakmaz: içerik 30 gün geri alınabilir, kalıcı silmede gider.
-- +goose StatementBegin
CREATE FUNCTION uploads_release_ref() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  old_ref uuid := (to_jsonb(OLD) ->> TG_ARGV[0])::uuid;
BEGIN
  IF old_ref IS NULL THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'UPDATE' AND old_ref IS NOT DISTINCT FROM (to_jsonb(NEW) ->> TG_ARGV[0])::uuid THEN
    RETURN NULL;
  END IF;
  UPDATE uploads SET status = 'deleting'
  WHERE id = old_ref AND status <> 'deleting';
  RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER tayfas_cover_check BEFORE INSERT OR UPDATE OF cover_upload_id ON tayfas
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('cover_upload_id', 'cover', 'tayfa');
CREATE TRIGGER events_cover_check BEFORE INSERT OR UPDATE OF cover_upload_id ON events
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('cover_upload_id', 'cover', 'event');
CREATE TRIGGER fishing_catches_photo_check BEFORE INSERT OR UPDATE OF photo_upload_id ON fishing_catches
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo_upload_id', 'catch_photo', 'fishing_catch');
CREATE TRIGGER tayfas_cover_release AFTER UPDATE OF cover_upload_id OR DELETE ON tayfas
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('cover_upload_id');
CREATE TRIGGER events_cover_release AFTER UPDATE OF cover_upload_id OR DELETE ON events
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('cover_upload_id');
CREATE TRIGGER fishing_catches_photo_release AFTER UPDATE OF photo_upload_id OR DELETE ON fishing_catches
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo_upload_id');

-- core_app (ADR-0015): kayıt açar, okur, siler (temizlik). Kimlik, sahip
-- dışı alanlar, anahtar, amaç, bildirilen tür ve boyut değiştirilemez: ele
-- geçirilmiş bir servis bile bir kaydı başka bir nesneye yönlendiremez.
-- user_id yalnızca hesap silmede NULL yapılır.
GRANT SELECT, INSERT, DELETE ON uploads TO core_app;
GRANT UPDATE (user_id, status, output_type, stored_bytes, width, height, reject_reason,
  attempts, run_after, attached_type, attached_id, attached_at, uploaded_at, ready_at, updated_at)
  ON uploads TO core_app;

-- +goose Down
DROP TRIGGER fishing_catches_photo_release ON fishing_catches;
DROP TRIGGER events_cover_release ON events;
DROP TRIGGER tayfas_cover_release ON tayfas;
DROP TRIGGER fishing_catches_photo_check ON fishing_catches;
DROP TRIGGER events_cover_check ON events;
DROP TRIGGER tayfas_cover_check ON tayfas;
DROP FUNCTION uploads_release_ref();
DROP FUNCTION uploads_check_ref();
ALTER TABLE fishing_catches DROP COLUMN photo_upload_id;
ALTER TABLE events DROP COLUMN cover_upload_id;
ALTER TABLE tayfas DROP COLUMN cover_upload_id;
DROP TABLE uploads;
