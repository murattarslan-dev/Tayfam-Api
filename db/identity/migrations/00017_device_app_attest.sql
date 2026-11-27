-- iOS App Attest (ADR-0022 §4, issue #195): girişte onaylanan anahtar
-- oturumun cihazına bağlanır; yenilemede bu anahtarın imzası (assertion)
-- doğrulanır ve sayacı saklanır.
--
--   app_attest_key_id      anahtar kimliği: açık anahtarın SHA-256'sı
--   app_attest_public_key  P-256 açık anahtar, sıkıştırılmamış (65 bayt)
--   app_attest_counter     son kabul edilen imzanın sayacı (yalnızca artar)
--
-- Apple'ın attestation nesnesi, sertifikaları ve receipt'i saklanmaz.
-- Anahtar kimliği tekildir: aynı anahtar iki cihaza (dolayısıyla iki hesaba)
-- bağlanamaz. Cihaz satırı silinince (hesap silme, 90 gün saklama) anahtar
-- da gider.
--
-- Geri alma planı: Down kolonları kaldırır; iOS oturumları bir sonraki
-- girişe kadar App Attest'siz (missing) sayılır.

-- +goose Up
ALTER TABLE devices
  ADD COLUMN app_attest_key_id     bytea,
  ADD COLUMN app_attest_public_key bytea,
  ADD COLUMN app_attest_counter    bigint,
  ADD CONSTRAINT devices_app_attest_key_id_key UNIQUE (app_attest_key_id),
  ADD CONSTRAINT devices_app_attest_all CHECK (
    (app_attest_key_id IS NULL AND app_attest_public_key IS NULL AND app_attest_counter IS NULL) OR
    (app_attest_key_id IS NOT NULL AND app_attest_public_key IS NOT NULL AND app_attest_counter IS NOT NULL)
  ),
  ADD CONSTRAINT devices_app_attest_shape CHECK (
    app_attest_key_id IS NULL OR (
      platform = 'ios' AND
      octet_length(app_attest_key_id) = 32 AND
      octet_length(app_attest_public_key) = 65 AND
      app_attest_counter BETWEEN 0 AND 4294967295
    )
  );

-- +goose Down
ALTER TABLE devices
  DROP CONSTRAINT devices_app_attest_shape,
  DROP CONSTRAINT devices_app_attest_all,
  DROP CONSTRAINT devices_app_attest_key_id_key,
  DROP COLUMN app_attest_counter,
  DROP COLUMN app_attest_public_key,
  DROP COLUMN app_attest_key_id;
