-- Şikâyetler ve tayfa moderasyon kuyruğu (issue #76; veri modeli v14
-- "Denetim", "Spam ve taciz", "Platforma yükselen şikâyetler", "Doğum Tarihi
-- ve Şikâyet Önceliği", "Saklama süreleri"; Notion "Kötüye Kullanım ve Genç
-- Kullanıcı Güvenliği", "Topluluk Kuralları"; ADR-0012).
--
-- reports: bir kullanıcının tayfa içindeki bir hedefi (üye, tayfanın
--   kendisi, duyuru, etkinlik, soru, cevap, anket, av kaydı) şikâyet
--   etmesi. Kanal ve birebir mesaj şikâyetleri chat işindedir.
--   - Aynı kişi aynı hedefi bir kez şikâyet eder (UNIQUE); ikinci şikâyet
--     mevcut kaydı döndürür.
--   - subject_user_id şikâyet edilen kişidir: hedef üyeyse kendisi, içerikse
--     yazarı (oluşturma anında). Kuyrukta bu kişiye şikâyet gösterilmez.
--   - Şikâyet edenin kimliği (reporter_id) yalnızca şikâyet edenin kendisine
--     ve ileride platform paneline döner; tayfa moderasyon kuyruğuna ve
--     şikâyet edilen kişiye hiçbir zaman dönmez. Oluşturma tayfanın
--     audit_logs'una yazılmaz (yöneticiler denetim kaydını görür).
--   - priority oluşturma anında hesaplanır ve sonradan değişmez (v14,
--     ADR-0012): core şikâyet eden ve şikâyet edilen için identity'ye
--     IsMinor sorar; biri 18 yaşından küçükse ya da identity'ye
--     ulaşılamazsa minor_involved. Yaş, doğum tarihi ve "18 altı" bilgisi
--     core'da hiçbir yerde saklanmaz; yalnızca şikâyetin önceliği kalır.
--   - priority yalnızca platform tarafında kullanılır. ADR-0012 gereği tayfa
--     yöneticileri ve moderatörleri 18 altı bilgisini görmez: tayfa kuyruğu
--     priority'yi döndürmez ve ona göre sıralamaz (yalnızca zamana göre).
--     minor_involved ve çocuk/genç güvenliği şikâyetleri oluşturuldukları
--     anda platforma yükselir.
--   - platform_only: hedefi tayfanın kendisi olan ya da bir yöneticinin veya
--     moderatörün işlemine dair (staff_conduct) şikâyetler tayfa kuyruğunda
--     görünmez, doğrudan platforma gider.
--   - escalated_to_platform_at: platforma yükselme. Sebep (escalation_reason)
--     tayfa hedefi, yönetici işlemi, çocuk/genç güvenliği, minor_involved
--     (oluşturma anında) ya da 7 gün çözülmeme (arka plan işi). Platform
--     paneli ayrı iştir; burada yalnızca alanlar var.
--   - Çözülmüş şikâyetler 1 yıl sonra silinir (arka plan işi). Platformda
--     açık olan şikâyet silinmez.
--
-- audit_logs saklama süresi (v14: 1 yıl): yalnızca ekleme kuralına tek bir
--   istisna eklenir. Saklama işi transaction'da app.retention_job =
--   'audit_logs' ayarlar; yalnızca 365 günden eski kayıtları silebilir.
--   Güncelleme yine reddedilir.
--
-- Kullanıcı kimlikleri identity'dedir; veritabanları arası foreign key
-- yoktur (ADR-0002). Hesap silinince reporter_id, subject_user_id,
-- handled_by ve platform_handled_by NULL olur (v14 "Kullanıcı → yazdığı
-- içerik: SET NULL").

-- +goose Up
CREATE TABLE reports (
  id                       uuid PRIMARY KEY,
  tayfa_id                 uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  reporter_id              uuid,
  target_type              text NOT NULL,
  target_id                uuid NOT NULL,
  subject_user_id          uuid,
  reason                   text NOT NULL,
  description              text,
  status                   text NOT NULL DEFAULT 'open',
  resolution               text,
  handled_by               uuid,
  handled_at               timestamptz,
  priority                 text NOT NULL DEFAULT 'normal',
  minor_involved_at        timestamptz,
  platform_only            boolean NOT NULL DEFAULT false,
  escalated_to_platform_at timestamptz,
  escalation_reason        text,
  platform_status          text,
  platform_handled_by      uuid,
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT reports_reporter_target_key UNIQUE (reporter_id, target_type, target_id),
  CONSTRAINT reports_target_type CHECK (target_type IN
    ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch')),
  CONSTRAINT reports_tayfa_target CHECK (target_type <> 'tayfa' OR target_id = tayfa_id),
  CONSTRAINT reports_reason CHECK (reason IN (
    'harassment', 'hate_speech', 'violence', 'privacy', 'impersonation',
    'illegal_trade', 'drugs', 'gambling', 'dangerous_activity', 'fake_event',
    'sexual_content', 'child_safety', 'spam', 'copyright', 'staff_conduct', 'other')),
  CONSTRAINT reports_description_len CHECK (description IS NULL OR char_length(description) BETWEEN 1 AND 1000),
  CONSTRAINT reports_status CHECK (status IN ('open', 'resolved')),
  CONSTRAINT reports_resolution CHECK (resolution IS NULL OR resolution IN ('content_removed', 'member_sanctioned', 'dismissed')),
  -- Açık şikâyette çözüm yoktur; çözülmüşte çözüm ve zamanı vardır
  -- (çözen, hesabı silinince NULL olabilir).
  CONSTRAINT reports_handled CHECK (
    (status = 'open' AND resolution IS NULL AND handled_at IS NULL AND handled_by IS NULL) OR
    (status = 'resolved' AND resolution IS NOT NULL AND handled_at IS NOT NULL)),
  CONSTRAINT reports_priority CHECK (priority IN ('normal', 'minor_involved')),
  CONSTRAINT reports_minor_involved_at CHECK ((priority = 'minor_involved') = (minor_involved_at IS NOT NULL)),
  CONSTRAINT reports_platform_only CHECK (platform_only = (target_type = 'tayfa' OR reason = 'staff_conduct')),
  CONSTRAINT reports_escalation CHECK (
    (escalated_to_platform_at IS NULL AND escalation_reason IS NULL AND platform_status IS NULL AND platform_handled_by IS NULL) OR
    (escalated_to_platform_at IS NOT NULL AND escalation_reason IS NOT NULL AND platform_status IS NOT NULL)),
  CONSTRAINT reports_escalation_reason CHECK (escalation_reason IS NULL OR escalation_reason IN
    ('tayfa_target', 'staff_conduct', 'child_safety', 'minor_involved', 'unresolved')),
  CONSTRAINT reports_platform_status CHECK (platform_status IS NULL OR platform_status IN ('open', 'resolved')),
  -- Platforma gitmesi gereken şikâyet oluşturulurken yükseltilmiş olmalıdır.
  CONSTRAINT reports_platform_only_escalated CHECK (NOT platform_only OR escalated_to_platform_at IS NOT NULL)
);
CREATE TRIGGER reports_updated_at BEFORE UPDATE ON reports FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Tayfa moderasyon kuyruğu (v14 "İndeksler": (tayfa_id, status)): açıklar
-- eskiden yeniye, çözülmüşler yeniden eskiye; önceliğe göre sıralanmaz.
CREATE INDEX reports_tayfa_open_idx ON reports (tayfa_id, created_at, id)
  WHERE status = 'open' AND NOT platform_only;
CREATE INDEX reports_tayfa_resolved_idx ON reports (tayfa_id, handled_at DESC, id DESC)
  WHERE status = 'resolved' AND NOT platform_only;
-- Platform paneli (v14: reports (priority, status, created_at)).
CREATE INDEX reports_priority_status_idx ON reports (priority, status, created_at);
-- Şikâyet edenin kendi şikâyetleri ve günlük sınır.
CREATE INDEX reports_reporter_idx ON reports (reporter_id, created_at DESC, id DESC) WHERE reporter_id IS NOT NULL;
-- Hesap silmede kullanıcı alanlarını boşaltmak için.
CREATE INDEX reports_subject_user_idx ON reports (subject_user_id) WHERE subject_user_id IS NOT NULL;
CREATE INDEX reports_handled_by_idx ON reports (handled_by) WHERE handled_by IS NOT NULL;
CREATE INDEX reports_platform_handled_by_idx ON reports (platform_handled_by) WHERE platform_handled_by IS NOT NULL;
-- 7 gün çözülmeyenlerin yükselmesi.
CREATE INDEX reports_escalation_due_idx ON reports (created_at) WHERE status = 'open' AND escalated_to_platform_at IS NULL;
-- Çözülmüşlerin 1 yıllık saklama süresi.
CREATE INDEX reports_retention_idx ON reports (handled_at) WHERE status = 'resolved';

-- Oluşturma anındaki bilgiler sonradan değişmez (priority dahil, v14).
-- Kullanıcı alanları yalnızca NULL'a döner (hesap silme). Çözülmüş şikâyet
-- yeniden açılmaz, çözümü değişmez; yükselme geri alınmaz.
-- +goose StatementBegin
CREATE FUNCTION reports_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.tayfa_id <> OLD.tayfa_id
     OR NEW.target_type <> OLD.target_type OR NEW.target_id <> OLD.target_id
     OR NEW.reason <> OLD.reason OR NEW.description IS DISTINCT FROM OLD.description
     OR NEW.priority <> OLD.priority OR NEW.minor_involved_at IS DISTINCT FROM OLD.minor_involved_at
     OR NEW.platform_only <> OLD.platform_only OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'şikâyetin oluşturma bilgileri değişmez' USING ERRCODE = 'TF100';
  END IF;
  IF (NEW.reporter_id IS DISTINCT FROM OLD.reporter_id AND NEW.reporter_id IS NOT NULL)
     OR (NEW.subject_user_id IS DISTINCT FROM OLD.subject_user_id AND NEW.subject_user_id IS NOT NULL) THEN
    RAISE EXCEPTION 'şikâyetin tarafları değişmez' USING ERRCODE = 'TF100';
  END IF;
  IF OLD.status = 'resolved' AND (NEW.status <> 'resolved' OR NEW.resolution <> OLD.resolution
     OR NEW.handled_at <> OLD.handled_at
     OR (NEW.handled_by IS DISTINCT FROM OLD.handled_by AND NEW.handled_by IS NOT NULL)) THEN
    RAISE EXCEPTION 'çözülmüş şikâyet değişmez' USING ERRCODE = 'TF100';
  END IF;
  IF OLD.escalated_to_platform_at IS NOT NULL AND (
       NEW.escalated_to_platform_at IS DISTINCT FROM OLD.escalated_to_platform_at
       OR NEW.escalation_reason IS DISTINCT FROM OLD.escalation_reason) THEN
    RAISE EXCEPTION 'platforma yükselme geri alınmaz' USING ERRCODE = 'TF100';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER reports_guard BEFORE UPDATE ON reports
  FOR EACH ROW EXECUTE FUNCTION reports_guard();

-- audit_logs: 00001'deki fonksiyon, saklama işinin 365 günden eski kayıtları
-- silebilmesi için genişletilir. Diğer bütün kurallar aynıdır.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  r audit_logs;
BEGIN
  IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM tayfas WHERE id = OLD.tayfa_id) THEN
    RETURN OLD;
  END IF;
  IF TG_OP = 'DELETE' AND current_setting('app.retention_job', true) = 'audit_logs'
     AND OLD.created_at < now() - interval '365 days' THEN
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

-- Saklama işi için.
CREATE INDEX audit_logs_created_at_idx ON audit_logs (created_at);

-- +goose Down
DROP INDEX audit_logs_created_at_idx;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger
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
DROP TABLE reports;
DROP FUNCTION reports_guard();
