-- KVKK veri dışa aktarma dosyası (issue #144; ADR-0007, ADR-0021; veri
-- modeli v14 "uploads": "data_export amacı KVKK işiyle ayrıca eklenecek").
--
-- Dosyayı identity şifreler; core yalnızca R2'deki yerini, boyutunu ve
-- süresini tutar ve imzalı linkleri üretir. Anahtar ve içerik core'a hiç
-- gelmez. Nesne exports/<r2_key> altındadır (incoming/ ve media/'dan ayrı
-- önek; R2 lifecycle kuralı exports-8d yedek temizliktir).
--
-- data_export bir görüntü değildir: varyant türleri ve boyutları, ölçüler,
-- tayfa ve bağlanma yoktur; boyut export_bytes'tadır ve ilk imzalı linkte
-- bir kez yazılır (trigger). client_request_id identity'deki talebin
-- kimliğidir (tekrar gönderim koruması). Kullanıcı başına en fazla bir
-- pending ya da ready dışa aktarma; hazır olunca 7 gün sonra (expires_at)
-- temizlik işi siler.
--
-- Geri alma planı: Down data_export kayıtlarını silme kuyruğuna değil
-- doğrudan siler (nesneleri R2 lifecycle kuralı 8 günde temizler), kolonları
-- ve kısıtları 00017'deki haline döndürür.

-- +goose Up
ALTER TABLE uploads
  ALTER COLUMN preview_type DROP NOT NULL,
  ALTER COLUMN preview_bytes DROP NOT NULL,
  ALTER COLUMN full_type DROP NOT NULL,
  ALTER COLUMN full_bytes DROP NOT NULL,
  ADD COLUMN export_bytes bigint,
  ADD COLUMN expires_at timestamptz;

ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover', 'data_export'));

-- Görüntülerin iki varyantı vardır; dışa aktarmanın hiçbiri yoktur.
ALTER TABLE uploads DROP CONSTRAINT uploads_types;
ALTER TABLE uploads DROP CONSTRAINT uploads_bytes;
ALTER TABLE uploads ADD CONSTRAINT uploads_variants CHECK (
  (purpose = 'data_export'
    AND preview_type IS NULL AND preview_bytes IS NULL AND full_type IS NULL AND full_bytes IS NULL
    AND width IS NULL AND attached_type IS NULL AND reject_reason IS NULL
    AND (export_bytes IS NULL OR export_bytes BETWEEN 30 AND 16777245))
  OR (purpose <> 'data_export'
    AND preview_type IN ('image/jpeg', 'image/webp') AND full_type IN ('image/jpeg', 'image/webp')
    -- Varyant başına en fazla 10 MiB (ADR-0007).
    AND preview_bytes BETWEEN 1 AND 10485760 AND full_bytes BETWEEN 1 AND 10485760
    AND export_bytes IS NULL AND expires_at IS NULL)
);
ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK ((purpose IN ('avatar', 'data_export')) = (tayfa_id IS NULL));
-- Hazır görüntünün ölçüleri, hazır dışa aktarmanın boyutu ve süresi vardır.
ALTER TABLE uploads DROP CONSTRAINT uploads_ready;
ALTER TABLE uploads ADD CONSTRAINT uploads_ready CHECK (
  status <> 'ready' OR (ready_at IS NOT NULL AND (
    (purpose = 'data_export' AND export_bytes IS NOT NULL AND expires_at IS NOT NULL)
    OR (purpose <> 'data_export' AND width IS NOT NULL)))
);

-- Kullanıcı başına tek etkin dışa aktarma.
CREATE UNIQUE INDEX uploads_one_export_key ON uploads (user_id)
  WHERE purpose = 'data_export' AND status IN ('pending', 'ready');
-- Temizlik: süresi dolan dışa aktarmalar.
CREATE INDEX uploads_export_expiry_idx ON uploads (expires_at) WHERE purpose = 'data_export' AND status = 'ready';

-- Boyut bir kez yazılır: imzalı PUT linki o boyuta bağlıdır; ele geçirilmiş
-- bir servis bile kaydı başka bir boyuta çeviremez.
-- +goose StatementBegin
CREATE FUNCTION uploads_export_bytes_once() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.export_bytes IS NOT NULL AND NEW.export_bytes IS DISTINCT FROM OLD.export_bytes THEN
    RAISE EXCEPTION 'dışa aktarma boyutu değiştirilemez' USING ERRCODE = 'TF081';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER uploads_export_bytes_once BEFORE UPDATE OF export_bytes ON uploads
  FOR EACH ROW EXECUTE FUNCTION uploads_export_bytes_once();

-- core_app (ADR-0015): boyut (bir kez) ve süre yazılır.
GRANT UPDATE (export_bytes, expires_at) ON uploads TO core_app;

-- +goose Down
REVOKE UPDATE (export_bytes, expires_at) ON uploads FROM core_app;
DROP TRIGGER uploads_export_bytes_once ON uploads;
DROP FUNCTION uploads_export_bytes_once();
DROP INDEX uploads_export_expiry_idx;
DROP INDEX uploads_one_export_key;
DELETE FROM uploads WHERE purpose = 'data_export';
ALTER TABLE uploads DROP CONSTRAINT uploads_ready;
ALTER TABLE uploads ADD CONSTRAINT uploads_ready CHECK (status <> 'ready' OR (width IS NOT NULL AND ready_at IS NOT NULL));
ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK ((purpose = 'avatar') = (tayfa_id IS NULL));
ALTER TABLE uploads DROP CONSTRAINT uploads_variants;
ALTER TABLE uploads ADD CONSTRAINT uploads_types CHECK (preview_type IN ('image/jpeg', 'image/webp') AND full_type IN ('image/jpeg', 'image/webp'));
ALTER TABLE uploads ADD CONSTRAINT uploads_bytes CHECK (preview_bytes BETWEEN 1 AND 10485760 AND full_bytes BETWEEN 1 AND 10485760);
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover'));
ALTER TABLE uploads
  DROP COLUMN expires_at,
  DROP COLUMN export_bytes,
  ALTER COLUMN preview_type SET NOT NULL,
  ALTER COLUMN preview_bytes SET NOT NULL,
  ALTER COLUMN full_type SET NOT NULL,
  ALTER COLUMN full_bytes SET NOT NULL;
