-- Keşfet ilk aşama: arkadaş ağı ve güvenlik ağı (issue #210, ADR-0033;
-- Notion "Kötüye Kullanım ve Genç Kullanıcı Güvenliği" → "Keşfet tasarımı",
-- "Faz 2 (v2) — Medya ve Sosyal" §6).
--
-- Mevcut alanların anlamı (şema Faz 1'den beri vardı):
--   tayfas.visibility = 'listed'   yöneticilerin Keşfet isteği; tek başına
--                                  listelemez, panel onayı da gerekir.
--   events.discoverable            organizatörün işareti: halka açık yerde
--                                  buluşur, Keşfet'te görünebilir (yalnızca
--                                  yüz yüze etkinlikte etkili).
--   memberships.joined_via         'discover': Keşfet'ten katılım isteği;
--                                  her zaman pending (trigger aşağıda).
--
-- tayfa_discover: başvuru ve panel kararı. status:
--   pending   inceleme bekliyor (ilk açılış, ret sonrası yeniden açılış ya
--             da onaylı tayfanın meta verisi değişti);
--   approved  panel meta veriyi onayladı;
--   rejected  panel reddetti ya da Keşfet'ten çıkardı;
--   dropped   otomatik düşürüldü (drop_reason), yalnızca panel kararıyla
--             döner. Nedeni tayfaya söylenmez (şikâyet edeni ele vermesin).
-- event_feedback: "Her şey yolunda mıydı?" cevabı; serbest metin yok.
-- event_safety_contacts: güvenlik kişisi; konum alanı yok.
--
-- Kurallar veritabanındadır, hiçbir yol atlayamaz (ADR-0011):
--   tayfa_discover_listable(tayfa)    listelenebilirliğin tek tanımı;
--   discover_friend_visible(izleyici, arkadaş, etkinlik, an)
--                                     "görünür arkadaş" kuralının tek tanımı
--                                     (akış ve Keşfet'ten katılım);
--   memberships_discover_guard        discover katılımı yalnızca pending ve
--                                     yalnızca listelenebilir tayfaya;
--   reports_discover_drop             Keşfet üyesinin şikâyeti tayfayı düşürür;
--   event_feedback_discover_drop      'serious' cevap tayfayı düşürür;
--   tayfas_discover_rereview          onaylı tayfanın adı, açıklaması, ili ya
--                                     da ilçesi değişirse yeniden incelemeye.
--
-- Anlık tayfalar (#209, ADR-0032) hiçbir zaman listelenmez: listelenebilirlik
-- kind = 'standard' ister; ayrıca 00027'deki tayfas_instant_private CHECK'i
-- anlık tayfayı visibility = 'private' tutar (ADR-0033 §12).
--
-- Kullanıcı kimlikleri identity'dedir; FK yoktur, user.deleted tüketicisi
-- temizler (ADR-0002).
--
-- Geri alma planı: Down trigger'ları, fonksiyonları ve tabloları kaldırır;
-- başvurular, panel kararları, geri bildirimler ve güvenlik kişileri
-- kaybolur. tayfas.visibility ve events.discoverable değerleri kalır.

-- +goose Up
CREATE TABLE tayfa_discover (
  tayfa_id          uuid PRIMARY KEY REFERENCES tayfas (id) ON DELETE CASCADE,
  status            text NOT NULL DEFAULT 'pending',
  drop_reason       text,
  requested_at      timestamptz NOT NULL,
  requested_by      uuid,
  reviewed_at       timestamptz,
  reviewed_by       uuid,
  status_changed_at timestamptz NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tayfa_discover_status CHECK (status IN ('pending', 'approved', 'rejected', 'dropped')),
  CONSTRAINT tayfa_discover_drop_reason CHECK (
    (status = 'dropped') = (drop_reason IS NOT NULL)
    AND (drop_reason IS NULL OR drop_reason IN ('member_report', 'negative_feedback'))),
  CONSTRAINT tayfa_discover_reviewed CHECK (status NOT IN ('approved', 'rejected') OR reviewed_at IS NOT NULL)
);
CREATE TRIGGER tayfa_discover_updated_at BEFORE UPDATE ON tayfa_discover FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- Panel kuyruğu.
CREATE INDEX tayfa_discover_queue_idx ON tayfa_discover (status_changed_at) WHERE status IN ('pending', 'dropped');

CREATE TABLE event_feedback (
  event_id   uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  answer     text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, user_id),
  CONSTRAINT event_feedback_answer CHECK (answer IN ('ok', 'not_ok', 'serious'))
);
CREATE TRIGGER event_feedback_updated_at BEFORE UPDATE ON event_feedback FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- Hesap silme, dışa aktarma ve saklama.
CREATE INDEX event_feedback_user_idx ON event_feedback (user_id);
CREATE INDEX event_feedback_created_idx ON event_feedback (created_at);

CREATE TABLE event_safety_contacts (
  event_id        uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  user_id         uuid NOT NULL,
  contact_user_id uuid NOT NULL,
  grace_minutes   integer NOT NULL DEFAULT 60,
  returned_at     timestamptz,
  alert_sent_at   timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, user_id),
  CONSTRAINT event_safety_contacts_not_self CHECK (user_id <> contact_user_id),
  CONSTRAINT event_safety_contacts_grace CHECK (grace_minutes BETWEEN 15 AND 1440)
);
CREATE TRIGGER event_safety_contacts_updated_at BEFORE UPDATE ON event_safety_contacts FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- Güvenlik kişisinin listesi ve hesap silme.
CREATE INDEX event_safety_contacts_contact_idx ON event_safety_contacts (contact_user_id);
CREATE INDEX event_safety_contacts_user_idx ON event_safety_contacts (user_id);
-- Alarm işi: henüz dönmemiş ve alarmı gitmemiş satırlar.
CREATE INDEX event_safety_contacts_pending_idx ON event_safety_contacts (event_id)
  WHERE returned_at IS NULL AND alert_sent_at IS NULL;

-- Bir tayfa şu an Keşfet'te listelenebilir mi (ADR-0033 §3): yöneticiler
-- açmış, panel onaylamış (düşürülmemiş), tayfa canlı ve yazılabilir, en az
-- 5 aktif üye. Etkinliğe ve izleyiciye özgü koşullar sorgulardadır.
-- +goose StatementBegin
CREATE FUNCTION tayfa_discover_listable(t uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1
    FROM tayfas x
    JOIN tayfa_discover d ON d.tayfa_id = x.id
    WHERE x.id = t
      AND x.kind = 'standard'
      AND x.visibility = 'listed'
      AND d.status = 'approved'
      AND x.status IN ('active', 'warned')
      AND x.suspended_at IS NULL
      AND x.deletion_requested_at IS NULL
      AND x.deleted_at IS NULL
      AND (SELECT count(*) FROM memberships m WHERE m.tayfa_id = x.id AND m.status = 'active') >= 5
  )
$$;
-- +goose StatementEnd

-- Arkadaş, izleyiciye Keşfet'te bu etkinlikte görünür mü (ADR-0033 §4).
-- "Görünür arkadaş" kuralının tek tanımıdır; akış ve Keşfet'ten katılım
-- aynı fonksiyonu kullanır:
--   - kabul edilmiş arkadaşlık, iki yönden engel yok, arkadaş silinmemiş ve
--     platformda banlı değil;
--   - arkadaş etkinliğe going demiş ya da katılımı attended; tayfanın aktif
--     üyesi;
--   - arkadaşın katılım geçmişi görünürlüğü private değil (ProfileService:
--     arkadaşlar tayfa ve public'te geçmişin tamamını görür);
--   - etkinlik "halka açık yer" işaretli, yüz yüze, planlı, silinmemiş,
--     bitmemiş ve 30 gün içinde başlıyor; tayfa listelenebilir;
--   - izleyicinin o tayfada hiçbir üyeliği yok (aktif, bekleyen, davetli ya
--     da banlı).
-- +goose StatementBegin
CREATE FUNCTION discover_friend_visible(viewer uuid, friend uuid, ev uuid, at timestamptz) RETURNS boolean
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
      AND NOT EXISTS (SELECT 1 FROM memberships vm WHERE vm.tayfa_id = e.tayfa_id AND vm.user_id = viewer)
      AND tayfa_discover_listable(e.tayfa_id)
  )
$$;
-- +goose StatementEnd

-- Keşfet'ten katılım yalnızca bekleyen istek olarak ve yalnızca
-- listelenebilir tayfaya eklenir (uygulama da denetler; ikinci savunma
-- hattı). memberships_guard katılım biçiminin geri kalanını denetler.
-- +goose StatementBegin
CREATE FUNCTION memberships_discover_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status <> 'pending' OR NOT tayfa_discover_listable(NEW.tayfa_id) THEN
    RAISE EXCEPTION 'Keşfet katılımı yalnızca listelenen tayfaya bekleyen istek olarak eklenir' USING ERRCODE = 'TF002';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER memberships_discover_guard BEFORE INSERT ON memberships
  FOR EACH ROW WHEN (NEW.joined_via = 'discover') EXECUTE FUNCTION memberships_discover_guard();

-- Keşfet'ten gelen üyenin (durumu ne olursa olsun) tayfadaki şikâyeti,
-- tayfayı askıdan önce Keşfet'ten düşürür.
-- +goose StatementBegin
CREATE FUNCTION reports_discover_drop() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.reporter_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM memberships m
       WHERE m.tayfa_id = NEW.tayfa_id AND m.user_id = NEW.reporter_id AND m.joined_via = 'discover') THEN
    UPDATE tayfa_discover
    SET status = 'dropped', drop_reason = 'member_report', status_changed_at = now()
    WHERE tayfa_id = NEW.tayfa_id AND status IN ('approved', 'pending');
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER reports_discover_drop AFTER INSERT ON reports
  FOR EACH ROW EXECUTE FUNCTION reports_discover_drop();

-- "Ciddi" olumsuz geri bildirim tayfayı inceleme bitene kadar düşürür.
-- +goose StatementBegin
CREATE FUNCTION event_feedback_discover_drop() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE tayfa_discover
  SET status = 'dropped', drop_reason = 'negative_feedback', status_changed_at = now()
  WHERE tayfa_id = (SELECT e.tayfa_id FROM events e WHERE e.id = NEW.event_id)
    AND status IN ('approved', 'pending');
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_feedback_discover_drop AFTER INSERT OR UPDATE OF answer ON event_feedback
  FOR EACH ROW WHEN (NEW.answer = 'serious') EXECUTE FUNCTION event_feedback_discover_drop();

-- Panel meta veriyi onaylamıştı; değişen meta veri onaysızdır.
-- +goose StatementBegin
CREATE FUNCTION tayfas_discover_rereview() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.name IS DISTINCT FROM OLD.name OR NEW.description IS DISTINCT FROM OLD.description
     OR NEW.city IS DISTINCT FROM OLD.city OR NEW.district IS DISTINCT FROM OLD.district THEN
    UPDATE tayfa_discover
    SET status = 'pending', requested_at = now(), status_changed_at = now()
    WHERE tayfa_id = NEW.id AND status = 'approved';
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER tayfas_discover_rereview AFTER UPDATE OF name, description, city, district ON tayfas
  FOR EACH ROW EXECUTE FUNCTION tayfas_discover_rereview();

-- core_app (ADR-0015): varsayılan yetki (00016) SELECT ve INSERT'tir.
-- tayfa_discover: durum ve inceleme alanları güncellenir; hesap silmede
--   kimlikler NULL olur; satır yalnızca tayfayla (CASCADE) gider.
-- event_feedback: cevap güncellenir; saklama ve hesap silmede silinir.
-- event_safety_contacts: kişi, tolerans, döndüm ve alarm zamanı
--   güncellenir; kaldırma, saklama ve hesap silmede silinir.
GRANT UPDATE (status, drop_reason, requested_at, requested_by, reviewed_at, reviewed_by, status_changed_at, updated_at)
  ON tayfa_discover TO core_app;
GRANT UPDATE (answer, updated_at), DELETE ON event_feedback TO core_app;
GRANT UPDATE (contact_user_id, grace_minutes, returned_at, alert_sent_at, updated_at), DELETE
  ON event_safety_contacts TO core_app;

-- +goose Down
DROP TRIGGER tayfas_discover_rereview ON tayfas;
DROP FUNCTION tayfas_discover_rereview();
DROP TRIGGER event_feedback_discover_drop ON event_feedback;
DROP FUNCTION event_feedback_discover_drop();
DROP TRIGGER reports_discover_drop ON reports;
DROP FUNCTION reports_discover_drop();
DROP TRIGGER memberships_discover_guard ON memberships;
DROP FUNCTION memberships_discover_guard();
DROP FUNCTION discover_friend_visible(uuid, uuid, uuid, timestamptz);
DROP FUNCTION tayfa_discover_listable(uuid);
DROP TABLE event_safety_contacts;
DROP TABLE event_feedback;
DROP TABLE tayfa_discover;
