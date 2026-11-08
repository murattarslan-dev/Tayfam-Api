-- identity veritabanı: kullanıcılar, giriş kimlikleri, cihazlar, oturumlar
-- (veri modeli v14, ADR-0004, ADR-0009, ADR-0012).
-- ID'ler UUIDv7 olarak uygulamada üretilir; zamanlar timestamptz (UTC).

-- +goose Up

-- Başka servislerin kullanıcıları bu veritabanına bağlanamaz; şemada da
-- varsayılan bir yetkileri yoktur (ADR-0002, ADR-0011).
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END
$$;
-- +goose StatementEnd
REVOKE ALL ON SCHEMA public FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TABLE users (
  id                    uuid PRIMARY KEY,
  display_name          text,
  avatar_upload_id      uuid,
  locale                text NOT NULL DEFAULT 'tr',
  -- Doğum tarihi uygulamada AES-256-GCM ile şifrelenir; veritabanı açık
  -- değeri hiçbir zaman görmez (ADR-0012). 16 yaş ve tarih aralığı kontrolü
  -- bu yüzden yalnızca Go'dadır.
  birth_date_enc        bytea,
  birth_date_visibility text NOT NULL DEFAULT 'birthday',
  birth_date_audience   text NOT NULL DEFAULT 'friends',
  activity_visibility   text NOT NULL DEFAULT 'tayfa',
  ref_code              text,
  ref_code_rotated_at   timestamptz,
  referred_by_user_id   uuid REFERENCES users (id) ON DELETE SET NULL,
  referred_at           timestamptz,
  -- Kayıt tamamlanınca (doğum tarihi ve onaylar) dolar; NULL iken hesap
  -- "kayıt bekliyor" durumundadır ve token yalnızca kayıt uçlarına erişir.
  registered_at         timestamptz,
  -- 16 yaşından küçük beyanla reddedilen kayıt. Doğum tarihinin kendisi
  -- saklanmaz; aynı giriş kimliğiyle başka bir tarih denenemez (ADR-0012).
  age_blocked_at        timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  deleted_at            timestamptz,

  CONSTRAINT users_display_name_len CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 40),
  CONSTRAINT users_display_name_present CHECK (deleted_at IS NOT NULL OR display_name IS NOT NULL),
  CONSTRAINT users_locale CHECK (locale IN ('tr')),
  CONSTRAINT users_birth_date_enc_len CHECK (birth_date_enc IS NULL OR octet_length(birth_date_enc) BETWEEN 30 AND 128),
  CONSTRAINT users_birth_date_visibility CHECK (birth_date_visibility IN ('hidden', 'birthday', 'full')),
  CONSTRAINT users_birth_date_audience CHECK (birth_date_audience IN ('friends', 'tayfa', 'public')),
  CONSTRAINT users_activity_visibility CHECK (activity_visibility IN ('private', 'tayfa', 'public')),
  CONSTRAINT users_ref_code_format CHECK (ref_code IS NULL OR ref_code ~ '^TYF-[2-9A-HJ-NP-Z]{6}$'),
  -- Aktif (kayıtlı, silinmemiş) hesabın doğum tarihi vardır.
  CONSTRAINT users_registered_has_birth_date CHECK (registered_at IS NULL OR deleted_at IS NOT NULL OR birth_date_enc IS NOT NULL),
  -- Yaş nedeniyle reddedilen hesap kayıtlı olamaz ve doğum tarihi tutmaz.
  CONSTRAINT users_age_blocked CHECK (age_blocked_at IS NULL OR (registered_at IS NULL AND birth_date_enc IS NULL)),
  CONSTRAINT users_no_self_referral CHECK (referred_by_user_id IS NULL OR referred_by_user_id <> id)
);
CREATE UNIQUE INDEX users_ref_code_key ON users (ref_code) WHERE ref_code IS NOT NULL;
CREATE INDEX users_referred_by_idx ON users (referred_by_user_id) WHERE referred_by_user_id IS NOT NULL;
CREATE INDEX users_deleted_at_idx ON users (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE auth_identities (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  provider     text NOT NULL,
  -- Sağlayıcının kullanıcı kimliği ("sub"); e-posta değişse de sabittir.
  subject      text NOT NULL,
  email        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT auth_identities_provider CHECK (provider IN ('google', 'apple')),
  CONSTRAINT auth_identities_subject_len CHECK (char_length(subject) BETWEEN 1 AND 255),
  CONSTRAINT auth_identities_email_len CHECK (email IS NULL OR char_length(email) BETWEEN 3 AND 320),
  CONSTRAINT auth_identities_provider_subject_key UNIQUE (provider, subject)
);
CREATE INDEX auth_identities_user_id_idx ON auth_identities (user_id);

-- Cihazın varlığının tek kaynağı. Push token'ı ve E2E anahtarları chat'tedir
-- (ADR-0009).
CREATE TABLE devices (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  platform     text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT devices_platform CHECK (platform IN ('android', 'ios', 'web'))
);
CREATE INDEX devices_user_id_idx ON devices (user_id);
CREATE INDEX devices_last_seen_at_idx ON devices (last_seen_at);

-- Bir oturum = bir refresh token ailesi (ADR-0004).
CREATE TABLE sessions (
  id                uuid PRIMARY KEY,
  user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  device_id         uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
  created_at        timestamptz NOT NULL DEFAULT now(),
  last_refreshed_at timestamptz NOT NULL DEFAULT now(),
  expires_at        timestamptz NOT NULL,
  revoked_at        timestamptz,
  revoked_reason    text,

  CONSTRAINT sessions_expiry CHECK (expires_at > created_at),
  CONSTRAINT sessions_revoked_reason CHECK (
    (revoked_at IS NULL AND revoked_reason IS NULL) OR
    (revoked_at IS NOT NULL AND revoked_reason IN ('logout', 'revoked_by_user', 'token_reuse', 'account_deleted', 'sanctioned', 'device_removed'))
  )
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_device_id_idx ON sessions (device_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- Refresh token'ın kendisi saklanmaz; yalnızca SHA-256 özeti. Veritabanı
-- sızsa da geçerli bir refresh token elde edilemez.
CREATE TABLE refresh_tokens (
  id         uuid PRIMARY KEY,
  session_id uuid NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
  token_hash bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at    timestamptz,

  CONSTRAINT refresh_tokens_hash_len CHECK (octet_length(token_hash) = 32),
  CONSTRAINT refresh_tokens_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX refresh_tokens_session_id_idx ON refresh_tokens (session_id);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE sessions;
DROP TABLE devices;
DROP TABLE auth_identities;
DROP TABLE users;
DROP FUNCTION set_updated_at();
