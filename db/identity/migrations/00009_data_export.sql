-- KVKK veri dışa aktarma talepleri (issue #144; ADR-0008, ADR-0021; veri
-- modeli v14 "data_export_requests", "Veri dışa aktarma").
--
-- Talep anında, kullanıcının kendi token'ıyla toplanır (ADR-0021 madde 3):
--   pending → processing → ready → expired
--                        ↘ failed (30 günlük sınırı yemez)
--
-- wrapped_key: dışa aktarma dosyasını şifreleyen 32 baytlık veri anahtarı,
--   identity'nin sürümlü sarma anahtarıyla (IDENTITY_DATA_EXPORT_KEYS,
--   AES-256-GCM) sarılı: sürüm (1) || nonce (12) || şifreli anahtar ve
--   etiket (48) = 61 bayt. Yalnızca ready'de doludur; 7 gün dolunca ya da
--   hesap silinince silinir (kripto-silme). Açık anahtar veritabanına hiç
--   girmez.
-- upload_id: core'daki şifreli dosyanın kaydı (uploads, purpose
--   data_export). Servisler arası foreign key yoktur (ADR-0002).
--
-- Satır 30 gün tutulur: "30 günde bir talep" sınırı requested_at'e bakar.
-- expired ve failed satırda kişisel veri kalmaz (yalnızca durum ve
-- zamanlar). Hesap silinince satırlar silme işleminde hemen silinir.
--
-- Notion v14'e göre eklenenler (ADR-0021 madde 8): failure_reason,
-- wrapped_key, size_bytes, started_at, finished_at, client_request_id.
--
-- Geri alma planı: Down tabloyu siler. Taleplerin geçmişi ve 7 gün içindeki
-- hazır dosyaların anahtarları kaybolur (dosyalar açılamaz hale gelir ve
-- core'un temizliğiyle silinir); kullanıcı yeni talep açabilir.

-- +goose Up
CREATE TABLE data_export_requests (
  id                uuid PRIMARY KEY,
  user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  status            text NOT NULL DEFAULT 'pending',
  failure_reason    text,
  upload_id         uuid,
  wrapped_key       bytea,
  size_bytes        bigint,
  client_request_id uuid NOT NULL,
  requested_at      timestamptz NOT NULL DEFAULT now(),
  started_at        timestamptz,
  finished_at       timestamptz,
  ready_at          timestamptz,
  expires_at        timestamptz,
  downloaded_at     timestamptz,

  CONSTRAINT data_export_requests_status CHECK (status IN ('pending', 'processing', 'ready', 'expired', 'failed')),
  CONSTRAINT data_export_requests_failure CHECK (
    (status = 'failed') = (failure_reason IS NOT NULL)
    AND (failure_reason IS NULL OR failure_reason IN ('unavailable', 'too_large', 'timeout', 'failed'))
  ),
  -- Hazır talebin dosyası, anahtarı, boyutu ve süresi vardır; anahtar
  -- yalnızca hazır talepte durur.
  CONSTRAINT data_export_requests_ready CHECK (
    (status = 'ready') = (wrapped_key IS NOT NULL)
    AND (status <> 'ready' OR (upload_id IS NOT NULL AND size_bytes IS NOT NULL AND ready_at IS NOT NULL AND expires_at IS NOT NULL))
  ),
  CONSTRAINT data_export_requests_expired CHECK (status <> 'expired' OR (upload_id IS NULL AND ready_at IS NOT NULL)),
  CONSTRAINT data_export_requests_key_len CHECK (wrapped_key IS NULL OR octet_length(wrapped_key) = 61),
  -- Şifreli dosya: en fazla 16 MiB zip + 29 bayt (sürüm, nonce, etiket).
  CONSTRAINT data_export_requests_size CHECK (size_bytes IS NULL OR size_bytes BETWEEN 30 AND 16777245),
  CONSTRAINT data_export_requests_started CHECK (status = 'pending' OR status = 'failed' OR started_at IS NOT NULL),
  CONSTRAINT data_export_requests_finished CHECK ((status IN ('pending', 'processing')) = (finished_at IS NULL)),
  CONSTRAINT data_export_requests_downloaded CHECK (downloaded_at IS NULL OR ready_at IS NOT NULL),
  CONSTRAINT data_export_requests_client_request_key UNIQUE (user_id, client_request_id)
);
-- Aynı anda tek etkin talep (pending, processing).
CREATE UNIQUE INDEX data_export_requests_one_active_key ON data_export_requests (user_id)
  WHERE status IN ('pending', 'processing');
-- Liste ve 30 günlük sınır.
CREATE INDEX data_export_requests_user_idx ON data_export_requests (user_id, requested_at DESC);
-- Saklama işi: süresi dolan hazır talepler, takılı kalanlar, 30 günlük satırlar.
CREATE INDEX data_export_requests_status_idx ON data_export_requests (status, requested_at);

-- identity_app (ADR-0015): talep açar, durumunu günceller, saklama işi ve
-- hesap silme satırları siler. Varsayılan yetki (00008) SELECT ve INSERT'tir.
GRANT UPDATE, DELETE ON data_export_requests TO identity_app;

-- +goose Down
DROP TABLE data_export_requests;
