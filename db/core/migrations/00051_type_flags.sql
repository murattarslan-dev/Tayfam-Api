-- Tür bayrakları: sensitive_attendance ve album_notice (issue #257;
-- ADR-0037 §1; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri
-- (2026-11-18)").
--
-- Bayraklar tür sürümünün tanımındadır (catalog_event_type_versions
-- .definition -> 'flags'); Go tarafında definition.Flags okur. SQL tarafında
-- tek kaynak event_attendance_sensitive(etkinlik) fonksiyonudur: katılımı
-- okuyan sorgular (arkadaşlık puanı, Keşfet'te "giden arkadaşlar", ortak
-- etkinlik geçmişi, ileride tayfa istatistiği #266) bayrağı buradan sorar.
-- Etkinlik oluşturulduğu tür sürümünde kalır; sürümler değişmez, bu yüzden
-- bayrak bir etkinlik için hiçbir zaman değişmez.
--
--   event_attendance_sensitive(ev)  etkinliğin tür sürümünde
--       sensitive_attendance açık mı. Bilinmeyen etkinlikte false.
--   user_activity_records_guard     ikinci savunma hattı (ADR-0011): hassas
--       katılımlı etkinliğin kişisel katılım geçmişine yazılmasını reddeder.
--       Uygulama zaten yazmaz.
--   discover_friend_visible         hassas katılımlı etkinlikte hiçbir
--       arkadaş "gidiyor" olarak görünmez.
--
-- album_notice yalnızca uygulamada denetlenir (albüm isteğinde
-- consent_ack); onay saklanmaz.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde bayrak
-- taşıyan tür yok; Kan bağışı ve Ziyaret katalog işiyle gelir).
--
-- Geri alma planı: Down tetikleyiciyi ve fonksiyonu kaldırır,
-- discover_friend_visible'ın 00029'daki gövdesini geri yazar. Veri
-- değişmez. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION event_attendance_sensitive(ev uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT COALESCE((
    SELECT (v.definition -> 'flags' -> 'sensitive_attendance') = 'true'::jsonb
    FROM events e
    JOIN catalog_event_type_versions v ON v.type_key = e.type AND v.version = e.type_version
    WHERE e.id = ev
  ), false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION user_activity_records_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.event_id IS NOT NULL AND event_attendance_sensitive(NEW.event_id) THEN
    RAISE EXCEPTION 'hassas katılımlı etkinlik katılım geçmişine yazılmaz'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'user_activity_records_sensitive';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER user_activity_records_guard
  BEFORE INSERT OR UPDATE OF event_id ON user_activity_records
  FOR EACH ROW EXECUTE FUNCTION user_activity_records_guard();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION discover_friend_visible(viewer uuid, friend uuid, ev uuid, at timestamptz) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT viewer <> friend AND discover_event_visible(viewer, ev, at) AND NOT event_attendance_sensitive(ev) AND EXISTS (
    SELECT 1
    FROM events e
    JOIN event_rsvps r ON r.event_id = e.id AND r.user_id = friend
    JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = friend AND m.status = 'active'
    JOIN user_profiles p ON p.user_id = friend AND p.deleted_at IS NULL AND p.activity_visibility <> 'private'
    JOIN friendships f ON f.user_low_id = LEAST(viewer, friend) AND f.user_high_id = GREATEST(viewer, friend)
                      AND f.status = 'accepted'
    WHERE e.id = ev
      AND (r.status = 'going' OR r.attendance = 'attended')
      AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                      WHERE (ub.blocker_id = viewer AND ub.blocked_id = friend)
                         OR (ub.blocker_id = friend AND ub.blocked_id = viewer))
      AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = friend AND b.banned_at IS NOT NULL)
      AND NOT EXISTS (SELECT 1 FROM social_settings ss WHERE ss.user_id = friend AND NOT ss.discover_visible_to_friends)
  )
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION discover_friend_visible(viewer uuid, friend uuid, ev uuid, at timestamptz) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT viewer <> friend AND discover_event_visible(viewer, ev, at) AND EXISTS (
    SELECT 1
    FROM events e
    JOIN event_rsvps r ON r.event_id = e.id AND r.user_id = friend
    JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = friend AND m.status = 'active'
    JOIN user_profiles p ON p.user_id = friend AND p.deleted_at IS NULL AND p.activity_visibility <> 'private'
    JOIN friendships f ON f.user_low_id = LEAST(viewer, friend) AND f.user_high_id = GREATEST(viewer, friend)
                      AND f.status = 'accepted'
    WHERE e.id = ev
      AND (r.status = 'going' OR r.attendance = 'attended')
      AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                      WHERE (ub.blocker_id = viewer AND ub.blocked_id = friend)
                         OR (ub.blocker_id = friend AND ub.blocked_id = viewer))
      AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = friend AND b.banned_at IS NOT NULL)
      AND NOT EXISTS (SELECT 1 FROM social_settings ss WHERE ss.user_id = friend AND NOT ss.discover_visible_to_friends)
  )
$$;
-- +goose StatementEnd

DROP TRIGGER user_activity_records_guard ON user_activity_records;
DROP FUNCTION user_activity_records_guard();
DROP FUNCTION event_attendance_sensitive(uuid);
