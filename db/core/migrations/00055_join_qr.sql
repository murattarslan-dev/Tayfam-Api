-- Şimdi başlat: QR ile katılım (issue #264; ADR-0038 §3, §5; Notion "Faz 3
-- — Katalog ayrıntıları ve şema ekleri (2026-11-18)" §D).
--
--   event_join_qr_tokens  kısa ömürlü katılım token'ları. Token rastgele
--       128 bittir (crypto/rand) ve yalnızca SHA-256 özeti saklanır; ömrü
--       2 dakikadır (CHECK). Satırlar 1 gün sonra retention işiyle silinir;
--       etkinlikle birlikte gider (cascade). Token'ı organizatör ya da
--       tayfanın yöneticisi açar (created_by; hesap silinince satırları
--       silinir).
--   event_join_qr_counts  etkinlik başına QR ile katılım sayacı (en fazla
--       30). Kim katıldığı tutulmaz; yalnızca sayı.
--   memberships.joined_via 'qr' eklenir. memberships_guard 'qr'ı 'code'
--       gibi ele alır (davet kimliği yok); anlık tayfada etkin katılım için
--       transaction'da app.join_qr_token ile verilen, süresi dolmamış ve
--       tayfanın aktif yöneticisinin açtığı token gerekir (ikinci savunma
--       hattı, ADR-0011). Kalıcı tayfada katılım politikası geçerlidir.
--   attendance_method 'qr': QR'ı okutan mevcut üyenin check-in'i.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down 'qr' ile kurulmuş üyelikler ya da 'qr' yöntemli
-- katılım kayıtları varken CHECK'ler yüzünden başarısız olur; önce bu
-- satırlar 'code' / 'location' olarak elle düzeltilmelidir (veri kaybı
-- yok, yalnızca kaynak bilgisi). Down tabloları, CHECK'leri ve
-- memberships_guard'ı 00014'teki haline döndürür. Uygulama kodu da
-- birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_join_qr_tokens (
  id         uuid PRIMARY KEY,
  event_id   uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  created_by uuid NOT NULL,
  token_hash bytea NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  CONSTRAINT event_join_qr_tokens_hash UNIQUE (token_hash),
  CONSTRAINT event_join_qr_tokens_hash_len CHECK (octet_length(token_hash) = 32),
  CONSTRAINT event_join_qr_tokens_ttl CHECK (expires_at > created_at AND expires_at <= created_at + interval '2 minutes')
);
CREATE INDEX event_join_qr_tokens_event_idx ON event_join_qr_tokens (event_id);
CREATE INDEX event_join_qr_tokens_created_idx ON event_join_qr_tokens (created_at);
CREATE INDEX event_join_qr_tokens_creator_idx ON event_join_qr_tokens (created_by);

CREATE TABLE event_join_qr_counts (
  event_id uuid PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
  joins    integer NOT NULL,
  CONSTRAINT event_join_qr_counts_joins CHECK (joins BETWEEN 1 AND 30)
);

GRANT SELECT, INSERT, DELETE ON event_join_qr_tokens TO core_app;
GRANT SELECT, INSERT ON event_join_qr_counts TO core_app;
GRANT UPDATE (joins) ON event_join_qr_counts TO core_app;

ALTER TABLE memberships DROP CONSTRAINT memberships_joined_via;
ALTER TABLE memberships ADD CONSTRAINT memberships_joined_via
  CHECK (joined_via IN ('creator', 'invite_link', 'direct_invite', 'code', 'discover', 'qr'));

ALTER TABLE event_rsvps DROP CONSTRAINT event_rsvps_attendance_method;
ALTER TABLE event_rsvps ADD CONSTRAINT event_rsvps_attendance_method
  CHECK (attendance_method IS NULL OR attendance_method IN ('location', 'online', 'hike_start', 'organizer', 'qr'));
ALTER TABLE user_activity_records DROP CONSTRAINT user_activity_records_attendance_method;
ALTER TABLE user_activity_records ADD CONSTRAINT user_activity_records_attendance_method
  CHECK (attendance_method IN ('location', 'online', 'hike_start', 'organizer', 'qr'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION memberships_guard() RETURNS trigger
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

  -- Sistem işi: platform yöneticisi (issue #104). Yalnızca aktif üyenin
  -- rolü değişir; ekleme, silme, durum ve diğer alanlar reddedilir. Son
  -- yönetici kuralı geçerlidir.
  IF job = 'platform_admin' THEN
    IF TG_OP <> 'UPDATE' THEN
      RAISE EXCEPTION 'platform yöneticisi üyelik ekleyemez veya silemez' USING ERRCODE = 'TF001';
    END IF;
    r := NEW;
    r.role := OLD.role;
    r.updated_at := OLD.updated_at;
    IF r IS DISTINCT FROM OLD OR OLD.status <> 'active' OR NEW.role = OLD.role THEN
      RAISE EXCEPTION 'platform yöneticisi yalnızca aktif üyenin rolünü değiştirir' USING ERRCODE = 'TF001';
    END IF;
    PERFORM memberships_check_last_admin(OLD, NEW.role, NEW.status, t.deleted_at IS NOT NULL, false);
    RETURN NEW;
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
      -- yönetici/moderatörün açtığı davet linki gerekir. QR (ADR-0038 §3)
      -- kod gibidir (davet kimliği yok); ayrıca anlık tayfada, bu
      -- transaction'ın verdiği (app.join_qr_token) süresi dolmamış ve
      -- tayfanın aktif yöneticisinin (organizatör) açtığı QR'la onaysız
      -- katılım olur.
      IF NEW.role = 'member' AND NEW.joined_via IN ('code', 'invite_link', 'discover', 'qr')
         AND (NEW.joined_via = 'invite_link') = (NEW.invite_id IS NOT NULL)
         AND (NEW.status = 'pending'
              OR (NEW.status = 'active' AND (
                    t.join_policy = 'open'
                    OR EXISTS (
                      SELECT 1 FROM invites i
                      JOIN memberships m ON m.tayfa_id = i.tayfa_id AND m.user_id = i.created_by
                      WHERE i.id = NEW.invite_id AND i.tayfa_id = tid
                        AND m.status = 'active' AND m.role IN ('admin', 'moderator'))
                    OR (NEW.joined_via = 'qr' AND EXISTS (
                      SELECT 1 FROM event_join_qr_tokens k
                      JOIN events e ON e.id = k.event_id
                      JOIN tayfas tf ON tf.id = e.tayfa_id
                      JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = k.created_by
                      WHERE k.id = NULLIF(current_setting('app.join_qr_token', true), '')::uuid
                        AND e.tayfa_id = tid AND tf.kind = 'instant' AND k.expires_at > now()
                        AND m.status = 'active' AND m.role = 'admin'))))) THEN
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

-- +goose Down

ALTER TABLE user_activity_records DROP CONSTRAINT user_activity_records_attendance_method;
ALTER TABLE user_activity_records ADD CONSTRAINT user_activity_records_attendance_method
  CHECK (attendance_method IN ('location', 'online', 'hike_start', 'organizer'));
ALTER TABLE event_rsvps DROP CONSTRAINT event_rsvps_attendance_method;
ALTER TABLE event_rsvps ADD CONSTRAINT event_rsvps_attendance_method
  CHECK (attendance_method IS NULL OR attendance_method IN ('location', 'online', 'hike_start', 'organizer'));

ALTER TABLE memberships DROP CONSTRAINT memberships_joined_via;
ALTER TABLE memberships ADD CONSTRAINT memberships_joined_via
  CHECK (joined_via IN ('creator', 'invite_link', 'direct_invite', 'code', 'discover'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION memberships_guard() RETURNS trigger
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

  -- Sistem işi: platform yöneticisi (issue #104). Yalnızca aktif üyenin
  -- rolü değişir; ekleme, silme, durum ve diğer alanlar reddedilir. Son
  -- yönetici kuralı geçerlidir.
  IF job = 'platform_admin' THEN
    IF TG_OP <> 'UPDATE' THEN
      RAISE EXCEPTION 'platform yöneticisi üyelik ekleyemez veya silemez' USING ERRCODE = 'TF001';
    END IF;
    r := NEW;
    r.role := OLD.role;
    r.updated_at := OLD.updated_at;
    IF r IS DISTINCT FROM OLD OR OLD.status <> 'active' OR NEW.role = OLD.role THEN
      RAISE EXCEPTION 'platform yöneticisi yalnızca aktif üyenin rolünü değiştirir' USING ERRCODE = 'TF001';
    END IF;
    PERFORM memberships_check_last_admin(OLD, NEW.role, NEW.status, t.deleted_at IS NOT NULL, false);
    RETURN NEW;
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

DROP TABLE event_join_qr_counts;
DROP TABLE event_join_qr_tokens;
