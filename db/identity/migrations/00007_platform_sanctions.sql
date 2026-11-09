-- Platform yaptırımları ve panelden kullanıcı yönetimi (issue #104 2. aşama;
-- veri modeli v14 "Platform Yönetimi ve Abonelik Altyapısı":
-- platform_sanctions; Notion "Platform Yönetim Paneli → Kurallar"; ADR-0004
-- "Platform yaptırımları").
--
-- platform_sanctions: platform genelinde askı (suspend, bitiş zamanı
--   zorunlu) veya ban (bitişsiz). Kullanıcı başına en fazla bir etkin
--   (kaldırılmamış) yaptırım. Etkin yaptırımda giriş ve token yenileme
--   reddedilir; süresi dolan askı kendiliğinden etkisiz olur. Kaldırma
--   lifted_at/lifted_by ile işaretlenir; satır silinmez (itiraz ve denetim
--   için). created_by ve lifted_by platform yöneticisidir.
--   Gerekçe denetim kaydında da vardır; kullanıcı silinince satırlar
--   CASCADE ile gider (kişisel kayıt), denetim kaydı bir yıl kalır.
--
-- sessions.revoked_reason: panelden "tüm oturumları kapat" için
--   'admin_revoked'.
--
-- E-postayla arama: auth_identities.email büyük/küçük harf duyarsız eşitlik
--   için ifade indeksi.
--
-- Geri alma planı: Down tabloyu ve indeksi siler; admin_revoked nedenli
-- oturumları 'sanctioned' olarak işaretleyip kısıtı 00005'teki haline
-- döndürür.

-- +goose Up
CREATE TABLE platform_sanctions (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  type       text NOT NULL,
  reason     text NOT NULL,
  starts_at  timestamptz NOT NULL DEFAULT now(),
  ends_at    timestamptz,
  created_by uuid NOT NULL,
  lifted_at  timestamptz,
  lifted_by  uuid,
  created_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT platform_sanctions_type CHECK (type IN ('suspend', 'ban')),
  CONSTRAINT platform_sanctions_reason_len CHECK (char_length(btrim(reason)) BETWEEN 10 AND 1000 AND char_length(reason) <= 1000),
  CONSTRAINT platform_sanctions_end CHECK ((type = 'ban') = (ends_at IS NULL) AND (ends_at IS NULL OR ends_at > starts_at)),
  CONSTRAINT platform_sanctions_lifted CHECK ((lifted_at IS NULL) = (lifted_by IS NULL) AND (lifted_at IS NULL OR lifted_at >= starts_at))
);
-- Kullanıcı başına tek etkin (kaldırılmamış) yaptırım. Süresi dolmuş askı
-- da kaldırılmadıkça satırı tutar; yeni yaptırımdan önce kaldırılır.
CREATE UNIQUE INDEX platform_sanctions_one_open_key ON platform_sanctions (user_id) WHERE lifted_at IS NULL;
CREATE INDEX platform_sanctions_user_idx ON platform_sanctions (user_id, created_at DESC);

ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'user_signout_remote', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed', 'age_blocked', 'admin_revoked'))
);

CREATE INDEX auth_identities_email_lower_idx ON auth_identities (lower(email)) WHERE email IS NOT NULL;

-- +goose Down
DROP INDEX auth_identities_email_lower_idx;
UPDATE sessions SET revoked_reason = 'sanctioned' WHERE revoked_reason = 'admin_revoked';
ALTER TABLE sessions DROP CONSTRAINT sessions_revoked_reason;
ALTER TABLE sessions ADD CONSTRAINT sessions_revoked_reason CHECK (
  (revoked_at IS NULL AND revoked_reason IS NULL) OR
  (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'user_signout_remote', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed', 'age_blocked'))
);
DROP TABLE platform_sanctions;
