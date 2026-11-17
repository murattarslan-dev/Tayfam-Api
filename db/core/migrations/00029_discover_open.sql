-- Keşfet herkese açık (issue #227, ADR-0035; platform sahibi 2026-11-17,
-- Notion "Kötüye Kullanım ve Genç Kullanıcı Güvenliği" → "Keşfet tasarımı"
-- → "2026-11-17 kararı"). ADR-0033 §4 ve §5'teki "en az bir görünür
-- arkadaş" şartı kalkar: listelenebilir tayfanın uygun etkinliğini herkes
-- görür. Arkadaş bilgisi ("Ayşe ve 2 arkadaşın") ve görünür arkadaş kuralı
-- aynen kalır.
--
-- Kurallar yine veritabanında, tek yerdedir (ADR-0011):
--   discover_event_visible(izleyici, etkinlik, an)
--     Etkinlik izleyicinin Keşfet'inde görünür mü. Arkadaştan bağımsız
--     koşulların tek tanımı: akış, Keşfet'ten katılım ve
--     discover_friend_visible bunu çağırır. ADR-0033'teki etkinlik ve tayfa
--     koşullarına bir kural eklenir (ADR-0035 §3, mimari oturumu kararı):
--     izleyiciyle tayfanın herhangi bir aktif üyesi arasında (iki yönden)
--     engel varsa tayfanın etkinlikleri izleyiciye görünmez ve izleyici
--     Keşfet'ten katılım isteyemez. Keşfet artık yabancılara açık olduğu
--     için engellenen kişinin, kendisini engelleyenin tayfasını Keşfet
--     üzerinden bulup katılmak istemesini önler.
--   discover_friend_visible(izleyici, arkadaş, etkinlik, an)
--     Aynı imza; gövdesi discover_event_visible + arkadaşa özgü koşullar.
--     Engelli/engelleyen kişi hiçbir zaman görünür arkadaş sayılmaz.
--
-- events_discover_idx: akış artık bütün uygun etkinlikleri tarar (eskiden
-- izleyicinin arkadaşlarının katılımlarından başlıyordu).
--
-- Geri alma planı: Down eski discover_friend_visible gövdesini geri
-- yazar, yeni fonksiyonu ve indeksi kaldırır. Veri değişmez.

-- +goose Up
CREATE INDEX events_discover_idx ON events (starts_at) WHERE discoverable AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION discover_event_visible(viewer uuid, ev uuid, at timestamptz) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1
    FROM events e
    WHERE e.id = ev
      AND e.discoverable AND e.mode = 'in_person' AND e.status = 'scheduled' AND e.deleted_at IS NULL
      AND e.ends_at > at AND e.starts_at < at + interval '30 days'
      AND NOT EXISTS (SELECT 1 FROM memberships vm WHERE vm.tayfa_id = e.tayfa_id AND vm.user_id = viewer)
      AND EXISTS (SELECT 1 FROM tayfa_modules tm
                  WHERE tm.tayfa_id = e.tayfa_id AND tm.module = 'events' AND tm.enabled)
      AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                      JOIN memberships bm ON bm.user_id = ub.blocked_id
                      WHERE ub.blocker_id = viewer AND bm.tayfa_id = e.tayfa_id AND bm.status = 'active')
      AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                      JOIN memberships bm ON bm.user_id = ub.blocker_id
                      WHERE ub.blocked_id = viewer AND bm.tayfa_id = e.tayfa_id AND bm.status = 'active')
      AND tayfa_discover_listable(e.tayfa_id)
  )
$$;
-- +goose StatementEnd

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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION discover_friend_visible(viewer uuid, friend uuid, ev uuid, at timestamptz) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT viewer <> friend AND EXISTS (
    SELECT 1
    FROM events e
    JOIN event_rsvps r ON r.event_id = e.id AND r.user_id = friend
    JOIN memberships m ON m.tayfa_id = e.tayfa_id AND m.user_id = friend AND m.status = 'active'
    JOIN user_profiles p ON p.user_id = friend AND p.deleted_at IS NULL AND p.activity_visibility <> 'private'
    JOIN friendships f ON f.user_low_id = LEAST(viewer, friend) AND f.user_high_id = GREATEST(viewer, friend)
                      AND f.status = 'accepted'
    WHERE e.id = ev
      AND (r.status = 'going' OR r.attendance = 'attended')
      AND e.discoverable AND e.mode = 'in_person' AND e.status = 'scheduled' AND e.deleted_at IS NULL
      AND e.ends_at > at AND e.starts_at < at + interval '30 days'
      AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                      WHERE (ub.blocker_id = viewer AND ub.blocked_id = friend)
                         OR (ub.blocker_id = friend AND ub.blocked_id = viewer))
      AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = friend AND b.banned_at IS NOT NULL)
      AND NOT EXISTS (SELECT 1 FROM social_settings ss WHERE ss.user_id = friend AND NOT ss.discover_visible_to_friends)
      AND NOT EXISTS (SELECT 1 FROM memberships vm WHERE vm.tayfa_id = e.tayfa_id AND vm.user_id = viewer)
      AND EXISTS (SELECT 1 FROM tayfa_modules tm
                  WHERE tm.tayfa_id = e.tayfa_id AND tm.module = 'events' AND tm.enabled)
      AND tayfa_discover_listable(e.tayfa_id)
  )
$$;
-- +goose StatementEnd
DROP FUNCTION discover_event_visible(uuid, uuid, timestamptz);
DROP INDEX events_discover_idx;
