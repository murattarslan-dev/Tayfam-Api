-- Hesap yönetimi (issue #75; veri modeli v14 "Saklama süreleri", "İlişkiler
-- ve Silme Davranışı"; ADR-0004).
--
-- sessions.revoked_reason: kullanıcının oturum listesinden kapattığı oturum
--   (tek oturum ya da "diğer tüm oturumlar") user_signout_remote ile
--   işaretlenir. Eski revoked_by_user değeri hiç yazılmadı; geriye uyum için
--   listede kalır.
--
-- Saklama işleri (internal/identity/app/retention) son görülme ve iptal
-- zamanına göre satır siler; aşağıdaki indeksler bu taramalar içindir.
-- devices.last_seen_at zaten indekslidir (00001).

-- +goose Up
ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'user_signout_remote', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed', 'age_blocked'))
);

CREATE INDEX sessions_revoked_at_idx ON sessions (revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX sessions_last_refreshed_at_idx ON sessions (last_refreshed_at);
CREATE INDEX refresh_tokens_used_at_idx ON refresh_tokens (used_at) WHERE used_at IS NOT NULL;

-- +goose Down
DROP INDEX refresh_tokens_used_at_idx;
DROP INDEX sessions_last_refreshed_at_idx;
DROP INDEX sessions_revoked_at_idx;
-- Geri almadan önce yeni nedenle kapatılmış oturumlar eski karşılığına
-- çevrilir; aksi halde eski CHECK eklenemez.
UPDATE sessions SET revoked_reason = 'revoked_by_user' WHERE revoked_reason = 'user_signout_remote';
ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed', 'age_blocked'))
);
