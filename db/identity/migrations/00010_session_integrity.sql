-- Uygulama bütünlüğü (ADR-0022, issue #163): oturumun resmi uygulamaya ait
-- olup olmadığının özeti. Ham bütünlük token'ı, Google'ın cevabı ve cihaz
-- bilgisi saklanmaz; yalnızca durum, kapalı listeden bir neden ve iki zaman.
--
--   unverified: hiç denetlenmedi, kanıt gelmedi ya da Google'a sorulamadı
--   passed:     son denetim geçti (integrity_passed_at)
--   failed:     son denetim geçmedi (integrity_reason)
--
-- Geri alma planı: Down kolonları kaldırır; oturumlara dokunmaz, yalnızca
-- bütünlük özeti kaybolur (bir sonraki denetimde yeniden oluşur).

-- +goose Up
ALTER TABLE sessions
  ADD COLUMN integrity_status     text NOT NULL DEFAULT 'unverified',
  ADD COLUMN integrity_reason     text,
  ADD COLUMN integrity_checked_at timestamptz,
  ADD COLUMN integrity_passed_at  timestamptz,
  ADD CONSTRAINT sessions_integrity_status CHECK (integrity_status IN ('unverified', 'passed', 'failed')),
  ADD CONSTRAINT sessions_integrity_reason CHECK (
    integrity_reason IS NULL OR
    integrity_reason IN ('missing', 'binding', 'stale', 'app_unrecognized', 'device', 'unavailable', 'malformed')
  ),
  -- Geçen oturumun nedeni yoktur, geçmeyeninki vardır; hiç denetlenmemiş
  -- oturumun nedeni yoktur.
  ADD CONSTRAINT sessions_integrity_reason_status CHECK (
    (integrity_status <> 'passed' OR integrity_reason IS NULL) AND
    (integrity_status <> 'failed' OR integrity_reason IS NOT NULL)
  ),
  ADD CONSTRAINT sessions_integrity_passed CHECK (
    integrity_status <> 'passed' OR integrity_passed_at IS NOT NULL
  ),
  ADD CONSTRAINT sessions_integrity_checked CHECK (
    (integrity_status = 'unverified' AND integrity_reason IS NULL) OR integrity_checked_at IS NOT NULL
  );

-- +goose Down
ALTER TABLE sessions
  DROP CONSTRAINT sessions_integrity_checked,
  DROP CONSTRAINT sessions_integrity_passed,
  DROP CONSTRAINT sessions_integrity_reason_status,
  DROP CONSTRAINT sessions_integrity_reason,
  DROP CONSTRAINT sessions_integrity_status,
  DROP COLUMN integrity_passed_at,
  DROP COLUMN integrity_checked_at,
  DROP COLUMN integrity_reason,
  DROP COLUMN integrity_status;
