-- core veritabanı: tayfalar, üyelikler, davetler, denetim kaydı ve outbox
-- (veri modeli v14 "Tayfa", "Denetim", "Rol hiyerarşisi", "Tayfa yönetimi";
-- ADR-0002, ADR-0004, ADR-0009, ADR-0011).
-- ID'ler UUIDv7 olarak uygulamada üretilir; zamanlar timestamptz (UTC).
-- Kullanıcı kimlikleri (user_id, created_by, actor_id …) identity'nin
-- verisidir; veritabanları arası foreign key olmadığı için bu kolonlar
-- referans değildir, silinen kullanıcı user.deleted olayıyla temizlenir
-- (ADR-0009).
--
-- Keşfet alanları (slug, approx_location, listed_at) ve kapak görseli bu
-- sürümde yok: keşfet Faz 2'dedir, kapak uploads tablosuyla gelir.

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

-- ---------------------------------------------------------------------------
-- İşlemi yapan kişi ve sistem işleri
--
-- Uygulama, üyelikleri değiştiren her transaction'da işlemi yapan kullanıcıyı
-- SET LOCAL app.actor_id ile bildirir (ADR-0004). Kullanıcısız arka plan
-- işleri bunun yerine app.system_job ile kendini tanıtır; tanınan tek değer
-- 'account_deletion'dır (identity'nin user.deleted olayı). İkisi de yoksa
-- üyelik değişiklikleri reddedilir.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION current_actor() RETURNS uuid
LANGUAGE plpgsql STABLE AS $$
DECLARE
  v text := current_setting('app.actor_id', true);
BEGIN
  IF v IS NULL OR v = '' THEN
    RETURN NULL;
  END IF;
  -- Geçersiz bir değer uuid'ye çevrilemez ve işlem hata ile durur.
  RETURN v::uuid;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION current_system_job() RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
  v text := current_setting('app.system_job', true);
BEGIN
  IF v IS NULL OR v = '' THEN
    RETURN NULL;
  END IF;
  IF v <> 'account_deletion' THEN
    RAISE EXCEPTION 'bilinmeyen sistem işi' USING ERRCODE = 'TF001';
  END IF;
  RETURN v;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION role_rank(r text) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE r WHEN 'admin' THEN 3 WHEN 'moderator' THEN 2 WHEN 'member' THEN 1 ELSE 0 END
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Tayfa
-- ---------------------------------------------------------------------------

CREATE TABLE tayfas (
  id                     uuid PRIMARY KEY,
  name                   text NOT NULL,
  -- strings.ToLowerSpecial(unicode.TurkishCase, name); uygulamada üretilir.
  name_normalized        text NOT NULL,
  description            text,
  join_code              text NOT NULL,
  status                 text NOT NULL DEFAULT 'active',
  visibility             text NOT NULL DEFAULT 'private',
  join_policy            text NOT NULL DEFAULT 'approval',
  city                   text,
  district               text,
  created_by             uuid,
  -- Aynı oluşturma isteğinin tekrar gönderilmesine karşı (CLAUDE.md).
  client_request_id      uuid,
  last_activity_at       timestamptz NOT NULL DEFAULT now(),
  warned_at              timestamptz,
  archived_at            timestamptz,
  -- Platform askısı (veri modeli v13); suspended_by platform yöneticisidir.
  suspended_at           timestamptz,
  suspended_reason       text,
  suspended_by           uuid,
  deletion_requested_at  timestamptz,
  deletion_requested_by  uuid,
  deletion_scheduled_for timestamptz,
  deleted_at             timestamptz,
  deleted_by             uuid,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tayfas_name_len CHECK (char_length(name) BETWEEN 3 AND 50),
  CONSTRAINT tayfas_name_normalized_len CHECK (char_length(name_normalized) BETWEEN 3 AND 50),
  CONSTRAINT tayfas_description_len CHECK (description IS NULL OR char_length(description) <= 500),
  -- 8 karakter; karıştırılan 0/O, 1/I/L hariç.
  CONSTRAINT tayfas_join_code_format CHECK (join_code ~ '^[2-9A-HJ-NP-Z]{8}$'),
  CONSTRAINT tayfas_status CHECK (status IN ('active', 'warned', 'archived')),
  -- Durum ile zaman damgası tutarlıdır (v14 "Tayfa yönetimi"): aktif tayfada
  -- uyarı ve arşiv izi kalmaz; uyarılan tayfa arşivlenmemiştir.
  CONSTRAINT tayfas_status_timestamps CHECK (
    (status = 'active'   AND warned_at IS NULL     AND archived_at IS NULL) OR
    (status = 'warned'   AND warned_at IS NOT NULL AND archived_at IS NULL) OR
    (status = 'archived' AND archived_at IS NOT NULL)
  ),
  CONSTRAINT tayfas_visibility CHECK (visibility IN ('private', 'listed')),
  CONSTRAINT tayfas_join_policy CHECK (join_policy IN ('approval', 'open')),
  CONSTRAINT tayfas_city_len CHECK (city IS NULL OR char_length(city) BETWEEN 1 AND 50),
  CONSTRAINT tayfas_district_len CHECK (district IS NULL OR char_length(district) BETWEEN 1 AND 50),
  CONSTRAINT tayfas_district_needs_city CHECK (district IS NULL OR city IS NOT NULL),
  CONSTRAINT tayfas_suspension CHECK (
    (suspended_at IS NULL AND suspended_reason IS NULL AND suspended_by IS NULL) OR
    (suspended_at IS NOT NULL AND suspended_reason IS NOT NULL AND char_length(suspended_reason) BETWEEN 1 AND 500)
  ),
  -- Silme talebi: talep zamanı ve planlanan silme birlikte bulunur; talep
  -- eden hesap silinirse deletion_requested_by NULL olabilir.
  CONSTRAINT tayfas_deletion_request CHECK (
    (deletion_requested_at IS NULL AND deletion_requested_by IS NULL AND deletion_scheduled_for IS NULL) OR
    (deletion_requested_at IS NOT NULL AND deletion_scheduled_for IS NOT NULL AND deletion_scheduled_for > deletion_requested_at)
  ),
  CONSTRAINT tayfas_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT tayfas_join_code_key UNIQUE (join_code)
);
CREATE UNIQUE INDEX tayfas_client_request_key ON tayfas (created_by, client_request_id) WHERE client_request_id IS NOT NULL;
-- Kullanıcı başına günlük tayfa açma sınırı (v14 "Spam ve taciz").
CREATE INDEX tayfas_created_by_idx ON tayfas (created_by, created_at);
-- Pasif tayfa temizliği.
CREATE INDEX tayfas_status_activity_idx ON tayfas (status, last_activity_at) WHERE deleted_at IS NULL;
-- 72 saatlik bekleme sonrası soft delete işi.
CREATE INDEX tayfas_deletion_scheduled_idx ON tayfas (deletion_scheduled_for) WHERE deletion_scheduled_for IS NOT NULL AND deleted_at IS NULL;
-- 30 günlük kalıcı silme işi.
CREATE INDEX tayfas_deleted_at_idx ON tayfas (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER tayfas_updated_at BEFORE UPDATE ON tayfas FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE tayfa_interests (
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  event_type text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tayfa_id, event_type),
  CONSTRAINT tayfa_interests_event_type CHECK (event_type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general'))
);

CREATE TABLE tayfa_modules (
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  module     text NOT NULL,
  enabled    boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tayfa_id, module),
  -- Açılıp kapatılabilen MVP modülleri (authz Module ile aynı); yeni modül
  -- migration ile eklenir. Sohbet modül değildir, her tayfada vardır.
  CONSTRAINT tayfa_modules_module CHECK (module IN ('announcements', 'events', 'qa', 'polls'))
);
CREATE TRIGGER tayfa_modules_updated_at BEFORE UPDATE ON tayfa_modules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Davet linkinin kendisi saklanmaz; yalnızca SHA-256 özeti. Veritabanı sızsa
-- da geçerli bir davet linki elde edilemez.
CREATE TABLE invites (
  id         uuid PRIMARY KEY,
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  token_hash bytea NOT NULL,
  created_by uuid,
  expires_at timestamptz NOT NULL,
  -- NULL: kullanım sınırı yok (süre yine geçerlidir).
  max_uses   integer,
  use_count  integer NOT NULL DEFAULT 0,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT invites_token_hash_len CHECK (octet_length(token_hash) = 32),
  CONSTRAINT invites_expiry CHECK (expires_at > created_at),
  CONSTRAINT invites_max_uses CHECK (max_uses IS NULL OR max_uses > 0),
  CONSTRAINT invites_use_count CHECK (use_count >= 0 AND (max_uses IS NULL OR use_count <= max_uses)),
  CONSTRAINT invites_token_hash_key UNIQUE (token_hash),
  -- memberships'in bileşik foreign key'i için: davet ile üyelik aynı tayfadadır.
  CONSTRAINT invites_tayfa_id_id_key UNIQUE (tayfa_id, id)
);
CREATE INDEX invites_tayfa_id_idx ON invites (tayfa_id, created_at DESC);

CREATE TABLE memberships (
  tayfa_id           uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  user_id            uuid NOT NULL,
  role               text NOT NULL DEFAULT 'member',
  status             text NOT NULL,
  joined_via         text NOT NULL,
  -- Hangi davet linkiyle girildiği (sızan link incelemesi). Davet silinince
  -- NULL olur; davet her zaman aynı tayfaya aittir.
  invite_id          uuid,
  invited_by_user_id uuid,
  approved_by        uuid,
  muted_until        timestamptz,
  muted_by           uuid,
  joined_at          timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tayfa_id, user_id),
  CONSTRAINT memberships_invite_fkey FOREIGN KEY (tayfa_id, invite_id)
    REFERENCES invites (tayfa_id, id) ON DELETE SET NULL (invite_id),
  CONSTRAINT memberships_role CHECK (role IN ('admin', 'moderator', 'member')),
  CONSTRAINT memberships_status CHECK (status IN ('invited', 'pending', 'active', 'banned')),
  CONSTRAINT memberships_joined_via CHECK (joined_via IN ('creator', 'invite_link', 'direct_invite', 'code', 'discover')),
  -- Henüz katılmamış kişinin rolü yoktur.
  CONSTRAINT memberships_role_before_join CHECK (status NOT IN ('invited', 'pending') OR role = 'member'),
  -- Aktif üyenin katılım zamanı vardır; henüz katılmamışın yoktur.
  CONSTRAINT memberships_joined_at CHECK (
    (status <> 'active' OR joined_at IS NOT NULL) AND
    (status NOT IN ('invited', 'pending') OR joined_at IS NULL)
  ),
  CONSTRAINT memberships_muted_by CHECK (muted_by IS NULL OR muted_until IS NOT NULL),
  CONSTRAINT memberships_no_self_invite CHECK (invited_by_user_id IS NULL OR invited_by_user_id <> user_id)
);
-- "Tayfalarım" ekranı.
CREATE INDEX memberships_user_id_status_idx ON memberships (user_id, status);
-- Onay bekleyenler.
CREATE INDEX memberships_pending_idx ON memberships (tayfa_id) WHERE status = 'pending';
-- Bir davetle girenler (davet iptalinde inceleme) ve bileşik FK.
CREATE INDEX memberships_invite_id_idx ON memberships (invite_id) WHERE invite_id IS NOT NULL;
CREATE TRIGGER memberships_updated_at BEFORE UPDATE ON memberships FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Üyelik koruması (ADR-0004, v14 "Rol hiyerarşisi", "Tayfa yönetimi")
--
-- Kural uygulama katmanında da uygulanır; bu trigger ikinci savunma hattıdır
-- ve uygulamadaki bir hatanın yetki yükseltmesine dönüşmesini engeller.
-- Rol sırası admin 3 › moderator 2 › member 1.
--
-- İşlemi yapan (app.actor_id) için:
--   Başkasına dokunma: aktör tayfada aktif üyedir ve hedefin şu anki rolü
--     kendisininkinden düşüktür. Atadığı rol kendi rolünden yüksek olamaz.
--     Durum geçişleri: pending→active (onay), pending/active/invited→banned.
--     Silme: başvuru reddi, çıkarma, ban kaldırma.
--     Başkası adına tek ekleme: doğrudan davet (status invited, rol member).
--   Kendine dokunma:
--     Ekleme: kurucu admin üyeliği (tayfayı açan, tayfada başka üyelik yok)
--       ya da member olarak katılım (kod, davet linki, keşfet). Doğrudan
--       active olmak için tayfa açık katılımlı olmalı ya da davet linkini
--       yönetici/moderatör açmış olmalıdır.
--     Güncelleme: daveti kabul (invited→active/pending) ve rolünü bırakma
--       (yalnızca düşürme). Kendini yükseltme, susturmayı kaldırma, onaylama
--       yok.
--     Silme: ayrılma, başvuruyu ya da daveti geri çekme. Banlı kişi kendi
--       ban kaydını silemez.
-- Aktör yoksa:
--   Tayfanın kendisi silinirken (ON DELETE CASCADE) üyelik silinir.
--   Davet silinirken (ON DELETE SET NULL) invite_id boşaltılır.
--   app.system_job = 'account_deletion': üyelik silme, aktif üyeyi admin
--     yapma (son yöneticinin halefi) ve silinen kullanıcının izlerini
--     (invited_by_user_id, approved_by, muted_by) NULL yapma. Başka bir şey
--     yapamaz.
--   Bunların dışında her değişiklik reddedilir.
-- Son yönetici: değişiklik sonrası tayfada aktif admin kalmıyorsa reddedilir
--   (tayfa siliniyorsa hariç). Hesap silme işinde tayfada başka aktif üye
--   kalmıyorsa izin verilir.
-- Eşzamanlılık: her değişiklik tayfa satırını kilitler (FOR NO KEY UPDATE);
--   aynı tayfadaki üyelik değişiklikleri sırayla işlenir, iki yöneticinin
--   aynı anda ayrılması tayfayı yöneticisiz bırakamaz.
--
-- Hata kodları (internal/core/store/errors.go): TF001 işlemi yapan yok ya da
-- sistem işine izin yok, TF002 rol hiyerarşisi, TF003 son yönetici.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION memberships_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  actor       uuid := current_actor();
  job         text := current_system_job();
  tid         uuid;
  t           record;
  tayfa_gone  boolean;
  actor_rank  int := 0;
  target_rank int;
  r           memberships;
BEGIN
  IF TG_OP = 'DELETE' THEN
    tid := OLD.tayfa_id;
  ELSE
    tid := NEW.tayfa_id;
  END IF;

  SELECT id, created_by, join_policy, deleted_at INTO t
  FROM tayfas WHERE id = tid
  FOR NO KEY UPDATE;
  tayfa_gone := NOT FOUND;

  -- Tayfanın kalıcı silinmesi (ON DELETE CASCADE): tayfa satırı bu
  -- transaction'da zaten silinmiştir.
  IF TG_OP = 'DELETE' AND tayfa_gone THEN
    RETURN OLD;
  END IF;

  -- Davetin silinmesi (ON DELETE SET NULL (invite_id)): yalnızca invite_id
  -- boşalır ve davet artık yoktur.
  IF TG_OP = 'UPDATE' AND OLD.invite_id IS NOT NULL AND NEW.invite_id IS NULL
     AND NOT EXISTS (SELECT 1 FROM invites WHERE id = OLD.invite_id) THEN
    r := NEW;
    r.invite_id := OLD.invite_id;
    r.updated_at := OLD.updated_at;
    IF r IS NOT DISTINCT FROM OLD THEN
      RETURN NEW;
    END IF;
  END IF;

  IF actor IS NOT NULL AND job IS NOT NULL THEN
    RAISE EXCEPTION 'işlemi yapan kişi ve sistem işi birlikte verilemez' USING ERRCODE = 'TF001';
  END IF;

  -- Sistem işi: hesap silme.
  IF job IS NOT NULL THEN
    IF TG_OP = 'DELETE' THEN
      PERFORM memberships_check_last_admin(OLD, NULL, NULL, t.deleted_at IS NOT NULL, true);
      RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
      IF (NEW.invited_by_user_id IS NOT NULL AND NEW.invited_by_user_id IS DISTINCT FROM OLD.invited_by_user_id)
         OR (NEW.approved_by IS NOT NULL AND NEW.approved_by IS DISTINCT FROM OLD.approved_by)
         OR (NEW.muted_by IS NOT NULL AND NEW.muted_by IS DISTINCT FROM OLD.muted_by)
         OR (NEW.role <> OLD.role AND NOT (NEW.role = 'admin' AND OLD.status = 'active')) THEN
        RAISE EXCEPTION 'sistem işi bu üyelik değişikliğini yapamaz' USING ERRCODE = 'TF001';
      END IF;
      r := NEW;
      r.invited_by_user_id := OLD.invited_by_user_id;
      r.approved_by := OLD.approved_by;
      r.muted_by := OLD.muted_by;
      r.role := OLD.role;
      r.updated_at := OLD.updated_at;
      IF r IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'sistem işi bu üyelik değişikliğini yapamaz' USING ERRCODE = 'TF001';
      END IF;
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'sistem işi üyelik ekleyemez' USING ERRCODE = 'TF001';
  END IF;

  -- Varsayılan: işlemi yapan bilinmiyorsa hiçbir değişiklik yapılmaz.
  IF actor IS NULL THEN
    RAISE EXCEPTION 'üyelik değişikliğinde işlemi yapan kişi belirtilmedi' USING ERRCODE = 'TF001';
  END IF;

  IF tayfa_gone THEN
    RAISE EXCEPTION 'tayfa yok' USING ERRCODE = 'TF002';
  END IF;

  SELECT role_rank(m.role) INTO actor_rank
  FROM memberships m
  WHERE m.tayfa_id = tid AND m.user_id = actor AND m.status = 'active';
  actor_rank := COALESCE(actor_rank, 0);

  -- ------------------------------------------------------------- INSERT
  IF TG_OP = 'INSERT' THEN
    IF NEW.approved_by IS NOT NULL OR NEW.muted_until IS NOT NULL OR NEW.muted_by IS NOT NULL THEN
      RAISE EXCEPTION 'yeni üyelik onaylı ya da susturulmuş başlayamaz' USING ERRCODE = 'TF002';
    END IF;

    IF NEW.user_id = actor THEN
      -- Kurucu: tayfayı açan kişi, ilk ve tek üyelik olarak admin.
      IF NEW.joined_via = 'creator' THEN
        IF NEW.role = 'admin' AND NEW.status = 'active' AND t.created_by = actor
           AND NEW.invite_id IS NULL AND NEW.invited_by_user_id IS NULL
           AND NOT EXISTS (SELECT 1 FROM memberships WHERE tayfa_id = tid) THEN
          RETURN NEW;
        END IF;
        RAISE EXCEPTION 'kurucu üyeliği yalnızca tayfayı açan kişi için ve ilk üyelik olarak eklenir' USING ERRCODE = 'TF002';
      END IF;

      -- Katılım: her zaman member; doğrudan active için açık katılım ya da
      -- yönetici/moderatörün açtığı davet linki gerekir.
      IF NEW.role = 'member' AND NEW.joined_via IN ('code', 'invite_link', 'discover')
         AND (NEW.joined_via = 'invite_link') = (NEW.invite_id IS NOT NULL)
         AND (NEW.status = 'pending'
              OR (NEW.status = 'active' AND (
                    t.join_policy = 'open'
                    OR EXISTS (
                      SELECT 1 FROM invites i
                      JOIN memberships m ON m.tayfa_id = i.tayfa_id AND m.user_id = i.created_by
                      WHERE i.id = NEW.invite_id AND i.tayfa_id = tid
                        AND m.status = 'active' AND m.role IN ('admin', 'moderator'))))) THEN
        RETURN NEW;
      END IF;
      RAISE EXCEPTION 'bu katılım biçimine izin yok' USING ERRCODE = 'TF002';
    END IF;

    -- Başkası adına ekleme: yalnızca aktif üyenin doğrudan daveti.
    IF actor_rank > 0 AND NEW.role = 'member' AND NEW.status = 'invited'
       AND NEW.joined_via = 'direct_invite' AND NEW.invited_by_user_id = actor
       AND NEW.invite_id IS NULL THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'başkası adına yalnızca doğrudan davet eklenebilir' USING ERRCODE = 'TF002';
  END IF;

  -- ------------------------------------------------------------- UPDATE
  IF TG_OP = 'UPDATE' THEN
    -- Kimlik ve kaynak alanları değişmez.
    IF NEW.tayfa_id <> OLD.tayfa_id OR NEW.user_id <> OLD.user_id
       OR NEW.joined_via <> OLD.joined_via
       OR NEW.invite_id IS DISTINCT FROM OLD.invite_id
       OR NEW.invited_by_user_id IS DISTINCT FROM OLD.invited_by_user_id
       OR NEW.created_at <> OLD.created_at THEN
      RAISE EXCEPTION 'üyeliğin kimlik alanları değiştirilemez' USING ERRCODE = 'TF002';
    END IF;

    IF OLD.user_id = actor THEN
      r := NEW;
      r.role := OLD.role;
      r.status := OLD.status;
      r.joined_at := OLD.joined_at;
      r.updated_at := OLD.updated_at;
      IF r IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'kişi kendi üyeliğinde bu alanı değiştiremez' USING ERRCODE = 'TF002';
      END IF;

      -- Daveti kabul.
      IF OLD.status = 'invited' AND NEW.role = OLD.role
         AND (NEW.status = 'pending'
              OR (NEW.status = 'active' AND (
                    t.join_policy = 'open'
                    OR EXISTS (
                      SELECT 1 FROM memberships m
                      WHERE m.tayfa_id = tid AND m.user_id = OLD.invited_by_user_id
                        AND m.status = 'active' AND m.role IN ('admin', 'moderator'))))) THEN
        RETURN NEW;
      END IF;

      -- Rolü bırakma: yalnızca düşürme.
      IF OLD.status = 'active' AND NEW.status = 'active'
         AND role_rank(NEW.role) < role_rank(OLD.role)
         AND NEW.joined_at IS NOT DISTINCT FROM OLD.joined_at THEN
        PERFORM memberships_check_last_admin(OLD, NEW.role, NEW.status, t.deleted_at IS NOT NULL, false);
        RETURN NEW;
      END IF;

      RAISE EXCEPTION 'kişi kendi üyeliğinde bu değişikliği yapamaz' USING ERRCODE = 'TF002';
    END IF;

    -- Başkasının üyeliği: hiyerarşi.
    target_rank := role_rank(OLD.role);
    IF actor_rank <= target_rank THEN
      RAISE EXCEPTION 'kendinden düşük olmayan role dokunulamaz' USING ERRCODE = 'TF002';
    END IF;

    IF NEW.role <> OLD.role THEN
      IF OLD.status <> 'active' OR NEW.status <> 'active' THEN
        RAISE EXCEPTION 'yalnızca aktif üyenin rolü değişir' USING ERRCODE = 'TF002';
      END IF;
      IF role_rank(NEW.role) > actor_rank THEN
        RAISE EXCEPTION 'kendinden yüksek rol atanamaz' USING ERRCODE = 'TF002';
      END IF;
    END IF;

    IF NEW.status <> OLD.status AND (OLD.status, NEW.status) NOT IN (
         ('pending', 'active'), ('pending', 'banned'), ('active', 'banned'), ('invited', 'banned')) THEN
      RAISE EXCEPTION 'bu durum geçişine izin yok' USING ERRCODE = 'TF002';
    END IF;

    -- Onaylayan, işlemi yapan kişidir; onay yalnızca başvuruda verilir.
    IF NEW.approved_by IS DISTINCT FROM OLD.approved_by
       AND NOT (OLD.status = 'pending' AND NEW.status = 'active' AND NEW.approved_by = actor) THEN
      RAISE EXCEPTION 'onaylayan işlemi yapan kişi olmalı' USING ERRCODE = 'TF002';
    END IF;
    IF OLD.status = 'pending' AND NEW.status = 'active' AND NEW.approved_by IS DISTINCT FROM actor THEN
      RAISE EXCEPTION 'onaylayan işlemi yapan kişi olmalı' USING ERRCODE = 'TF002';
    END IF;
    IF NEW.joined_at IS DISTINCT FROM OLD.joined_at
       AND NOT (OLD.status = 'pending' AND NEW.status = 'active') THEN
      RAISE EXCEPTION 'katılım zamanı yalnızca onayda yazılır' USING ERRCODE = 'TF002';
    END IF;

    -- Susturan, işlemi yapan kişidir; susturma kaldırılınca ikisi de boşalır.
    IF NEW.muted_until IS DISTINCT FROM OLD.muted_until OR NEW.muted_by IS DISTINCT FROM OLD.muted_by THEN
      IF NOT ((NEW.muted_until IS NOT NULL AND NEW.muted_by = actor)
              OR (NEW.muted_until IS NULL AND NEW.muted_by IS NULL)) THEN
        RAISE EXCEPTION 'susturan işlemi yapan kişi olmalı' USING ERRCODE = 'TF002';
      END IF;
    END IF;

    PERFORM memberships_check_last_admin(OLD, NEW.role, NEW.status, t.deleted_at IS NOT NULL, false);
    RETURN NEW;
  END IF;

  -- ------------------------------------------------------------- DELETE
  IF OLD.user_id = actor THEN
    IF OLD.status = 'banned' THEN
      RAISE EXCEPTION 'banlı kişi kendi ban kaydını silemez' USING ERRCODE = 'TF002';
    END IF;
  ELSIF actor_rank <= role_rank(OLD.role) THEN
    RAISE EXCEPTION 'kendinden düşük olmayan role dokunulamaz' USING ERRCODE = 'TF002';
  END IF;
  PERFORM memberships_check_last_admin(OLD, NULL, NULL, t.deleted_at IS NOT NULL, false);
  RETURN OLD;
END
$$;
-- +goose StatementEnd

-- new_role ve new_status NULL ise üyelik siliniyordur. Tayfa kilidi çağıran
-- trigger'dadır.
-- +goose StatementBegin
CREATE FUNCTION memberships_check_last_admin(old_row memberships, new_role text, new_status text, tayfa_deleted boolean, account_deletion boolean) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  IF tayfa_deleted OR old_row.status <> 'active' OR old_row.role <> 'admin' THEN
    RETURN;
  END IF;
  IF new_status = 'active' AND new_role = 'admin' THEN
    RETURN;
  END IF;
  IF EXISTS (
    SELECT 1 FROM memberships
    WHERE tayfa_id = old_row.tayfa_id AND user_id <> old_row.user_id
      AND status = 'active' AND role = 'admin') THEN
    RETURN;
  END IF;
  -- Hesabı silinen son kişi: tayfada devralacak aktif üye yoktur.
  IF account_deletion AND NOT EXISTS (
    SELECT 1 FROM memberships
    WHERE tayfa_id = old_row.tayfa_id AND user_id <> old_row.user_id AND status = 'active') THEN
    RETURN;
  END IF;
  RAISE EXCEPTION 'tayfada aktif yönetici kalmaz' USING ERRCODE = 'TF003';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER memberships_guard BEFORE INSERT OR UPDATE OR DELETE ON memberships
  FOR EACH ROW EXECUTE FUNCTION memberships_guard();

-- ---------------------------------------------------------------------------
-- Denetim kaydı: yalnızca ekleme (v14 "Denetim").
-- metadata'ya kişisel veri (mesaj metni, konum, e-posta, token) yazılmaz;
-- yalnızca kimlikler ve sınıflandırma bilgisi.
-- ---------------------------------------------------------------------------

CREATE TABLE audit_logs (
  id          uuid PRIMARY KEY,
  tayfa_id    uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- NULL: sistem işi ya da hesabı silinmiş kişi.
  actor_id    uuid,
  action      text NOT NULL,
  target_type text,
  target_id   uuid,
  metadata    jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT audit_logs_action_format CHECK (action ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND char_length(action) <= 64),
  CONSTRAINT audit_logs_target_type_format CHECK (target_type IS NULL OR (target_type ~ '^[a-z][a-z_]*$' AND char_length(target_type) <= 32)),
  CONSTRAINT audit_logs_target CHECK ((target_type IS NULL) = (target_id IS NULL)),
  CONSTRAINT audit_logs_metadata CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 4096)
);
CREATE INDEX audit_logs_tayfa_id_created_at_idx ON audit_logs (tayfa_id, created_at DESC);
-- Hesap silmede actor_id'yi boşaltma.
CREATE INDEX audit_logs_actor_id_idx ON audit_logs (actor_id) WHERE actor_id IS NOT NULL;

-- Güncelleme ve silme reddedilir. İki dar istisna: tayfanın kalıcı silinmesi
-- (ON DELETE CASCADE, tayfa satırı artık yok) ve hesap silme işinde
-- actor_id'nin NULL yapılması.
-- +goose StatementBegin
CREATE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  r audit_logs;
BEGIN
  IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM tayfas WHERE id = OLD.tayfa_id) THEN
    RETURN OLD;
  END IF;
  IF TG_OP = 'UPDATE' AND current_system_job() = 'account_deletion'
     AND OLD.actor_id IS NOT NULL AND NEW.actor_id IS NULL THEN
    r := NEW;
    r.actor_id := OLD.actor_id;
    IF r IS NOT DISTINCT FROM OLD THEN
      RETURN NEW;
    END IF;
  END IF;
  RAISE EXCEPTION 'denetim kaydı değiştirilemez' USING ERRCODE = 'TF004';
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION audit_logs_no_truncate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'denetim kaydı değiştirilemez' USING ERRCODE = 'TF004';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_logs_append_only BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION audit_logs_append_only();
CREATE TRIGGER audit_logs_no_truncate BEFORE TRUNCATE ON audit_logs
  FOR EACH STATEMENT EXECUTE FUNCTION audit_logs_no_truncate();

-- ---------------------------------------------------------------------------
-- Outbox (ADR-0002, veri modeli v10). Olay, iş verisiyle aynı transaction'da
-- yazılır; relay ayrı bir iştir. payload'a kişisel içerik yazılmaz.
-- tayfa_id bilerek foreign key değildir: tayfa.purged gibi olaylar tayfa
-- silindikten sonra da okunur.
-- ---------------------------------------------------------------------------

CREATE TABLE domain_events (
  id           uuid PRIMARY KEY,
  type         text NOT NULL,
  actor_id     uuid,
  tayfa_id     uuid,
  subject_type text,
  subject_id   uuid,
  payload      jsonb NOT NULL DEFAULT '{}',
  occurred_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT domain_events_type_format CHECK (type ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND char_length(type) <= 64),
  CONSTRAINT domain_events_subject_type_format CHECK (subject_type IS NULL OR (subject_type ~ '^[a-z][a-z_]*$' AND char_length(subject_type) <= 32)),
  CONSTRAINT domain_events_subject CHECK ((subject_type IS NULL) = (subject_id IS NULL)),
  CONSTRAINT domain_events_payload CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 8192)
);
CREATE INDEX domain_events_type_id_idx ON domain_events (type, id);
CREATE INDEX domain_events_actor_id_idx ON domain_events (actor_id, id) WHERE actor_id IS NOT NULL;

CREATE TABLE event_consumers (
  consumer_name text PRIMARY KEY,
  last_event_id uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_consumers_name_format CHECK (consumer_name ~ '^[a-z][a-z0-9_.-]{0,62}$')
);
CREATE TRIGGER event_consumers_updated_at BEFORE UPDATE ON event_consumers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE event_consumers;
DROP TABLE domain_events;
DROP TABLE audit_logs;
DROP FUNCTION audit_logs_no_truncate();
DROP FUNCTION audit_logs_append_only();
DROP TABLE memberships;
DROP FUNCTION memberships_check_last_admin(memberships, text, text, boolean, boolean);
DROP FUNCTION memberships_guard();
DROP TABLE invites;
DROP TABLE tayfa_modules;
DROP TABLE tayfa_interests;
DROP TABLE tayfas;
DROP FUNCTION role_rank(text);
DROP FUNCTION current_system_job();
DROP FUNCTION current_actor();
DROP FUNCTION set_updated_at();
