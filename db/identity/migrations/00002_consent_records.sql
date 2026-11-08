-- Aydınlatma metni ve kullanım koşulları onayları (veri modeli v14, ADR-0008).
-- Kayıtlar yalnızca eklenir; geri çekme withdrawn_at ile işaretlenir.
--
-- user_id bilerek foreign key değildir: hesap silindikten sonra onay
-- kayıtlarının ne kadar saklanacağı hukukçuyla belirlenecek (Notion, Açık
-- Kararlar). users satırının 30 günlük kalıcı silinmesi bu kayıtları kendi
-- kendine silmesin; saklama süresi kararlaşınca ayrı bir işle uygulanır.

-- +goose Up
CREATE TABLE consent_records (
  id               uuid PRIMARY KEY,
  user_id          uuid NOT NULL,
  document_type    text NOT NULL,
  document_version text NOT NULL,
  accepted_at      timestamptz NOT NULL DEFAULT now(),
  withdrawn_at     timestamptz,

  CONSTRAINT consent_records_document_type CHECK (document_type IN ('privacy_notice', 'terms')),
  CONSTRAINT consent_records_version_format CHECK (document_version ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}(\.[0-9]+)?$'),
  CONSTRAINT consent_records_withdrawn_after CHECK (withdrawn_at IS NULL OR withdrawn_at >= accepted_at),
  CONSTRAINT consent_records_user_document_version_key UNIQUE (user_id, document_type, document_version)
);
CREATE INDEX consent_records_user_id_idx ON consent_records (user_id);

-- Yaş sınırı nedeniyle reddedilen kayıtta oturumlar bu nedenle kapanır.
ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed', 'age_blocked'))
);

-- +goose Down
ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed'))
);
DROP TABLE consent_records;
