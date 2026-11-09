-- Platform yönetim paneli, core tarafı (issue #104 2. aşama; Notion
-- "Platform Yönetim Paneli", "Kötüye Kullanım ve Genç Kullanıcı Güvenliği";
-- ADR-0008).
--
-- 1. app.system_job = 'platform_admin': panelden rol düzeltme ve sahipsiz
--    tayfaya yönetici atama. Platform yöneticisi tayfanın üyesi değildir;
--    app.actor_id ile yapılan hiyerarşi kontrolü ona uygulanamaz. Dar bir
--    istisna: yalnızca AKTİF üyenin ROLÜ değişir (ekleme, silme, durum,
--    susturma, onay yok) ve son yönetici kuralı yine geçerlidir. core'da bu
--    değeri yalnızca internal/core/app/admin, admin token'ı ve identity'nin
--    yöneticilik onayından sonra ayarlar. audit_logs'un hesap silme
--    istisnası 'account_deletion'a bağlıdır; bu değer onu açmaz.
-- 2. reports: platform kararı (platform_decision, platform_handled_at).
--    Platformda çözülmüş şikâyetin kararı değişmez (reports_platform_guard).
-- 3. user_platform_bans: platform banlı kullanıcılar (identity'nin
--    user.banned / user.unbanned olayları; profil kopyası olmayan kullanıcı
--    için de tutulabilsin diye user_profiles'tan ayrı). event_id sıralama
--    içindir: eski olay yenisini ezmez. banned_at NULL ise ban kaldırılmış.
--    İçerik silinmez; gizlenir.
--
-- Geri alma planı: Down fonksiyonları 00001'deki haline döndürür,
-- platform_admin ile yapılmış değişiklikleri geri almaz (rol değişikliği
-- veri olarak kalır), kolonları ve tetikleyiciyi siler.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION current_system_job() RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
  v text := current_setting('app.system_job', true);
BEGIN
  IF v IS NULL OR v = '' THEN
    RETURN NULL;
  END IF;
  IF v NOT IN ('account_deletion', 'platform_admin') THEN
    RAISE EXCEPTION 'bilinmeyen sistem işi' USING ERRCODE = 'TF001';
  END IF;
  RETURN v;
END
$$;
-- +goose StatementEnd

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

ALTER TABLE reports
  ADD COLUMN platform_decision text,
  ADD COLUMN platform_handled_at timestamptz;
ALTER TABLE reports ADD CONSTRAINT reports_platform_decision CHECK (
  platform_decision IS NULL OR platform_decision IN ('content_removed', 'warned', 'tayfa_suspended', 'user_banned', 'dismissed'));
ALTER TABLE reports ADD CONSTRAINT reports_platform_resolved CHECK (
  (platform_status = 'resolved') = (platform_decision IS NOT NULL AND platform_handled_at IS NOT NULL));

-- +goose StatementBegin
CREATE FUNCTION reports_platform_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.platform_status = 'resolved' AND (
       NEW.platform_status IS DISTINCT FROM OLD.platform_status
       OR NEW.platform_decision IS DISTINCT FROM OLD.platform_decision
       OR NEW.platform_handled_at IS DISTINCT FROM OLD.platform_handled_at
       OR (NEW.platform_handled_by IS DISTINCT FROM OLD.platform_handled_by AND NEW.platform_handled_by IS NOT NULL)) THEN
    RAISE EXCEPTION 'platform kararı değişmez' USING ERRCODE = 'TF100';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER reports_platform_guard BEFORE UPDATE ON reports
  FOR EACH ROW EXECUTE FUNCTION reports_platform_guard();
-- Platform kuyruğu: açıklar, önce minor_involved ve child_safety, sonra eski.
CREATE INDEX reports_platform_queue_idx ON reports ((platform_status = 'open') DESC, (priority = 'minor_involved' OR reason = 'child_safety') DESC, escalated_to_platform_at, id)
  WHERE escalated_to_platform_at IS NOT NULL;

CREATE TABLE user_platform_bans (
  user_id   uuid PRIMARY KEY,
  banned_at timestamptz,
  event_id  uuid NOT NULL
);
CREATE INDEX user_platform_bans_banned_idx ON user_platform_bans (user_id) WHERE banned_at IS NOT NULL;

-- +goose Down
DROP TABLE user_platform_bans;
DROP INDEX reports_platform_queue_idx;
DROP TRIGGER reports_platform_guard ON reports;
DROP FUNCTION reports_platform_guard();
ALTER TABLE reports DROP CONSTRAINT reports_platform_resolved, DROP CONSTRAINT reports_platform_decision;
ALTER TABLE reports DROP COLUMN platform_handled_at, DROP COLUMN platform_decision;
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
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION current_system_job() RETURNS text
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
