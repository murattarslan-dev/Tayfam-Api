-- Sohbet ekleri (issue #205, ADR-0031): kanal fotoğrafı, uçtan uca şifreli
-- birebir ek ve birebir mesaj şikâyetinin kanıt fotoğrafı.
--
-- chat_photo: kanal mesajındaki fotoğraf; görüntü hattının aynısı (iki
-- varyant, başlık denetimi), tayfası vardır. chat mesajı yazarken bağlar
-- (attached_type = 'channel_message', attached_id = chat'in mesaj kimliği).
-- Kimin görebileceğine chat karar verir (kanal kitlesi).
--
-- dm_attachment: istemcinin rastgele anahtarla şifrelediği opak blob. Bir
-- görüntü değildir: önizleme yoktur, tek nesne full_* alanlarındadır
-- (application/octet-stream, 1 B – 10 MiB); ölçü, tür ve metadata
-- denetimi yapılmaz. Anahtar ve özet Signal zarfının içindedir, burada
-- yoktur. chat birebir mesaja bağlar (attached_type = 'dm_message').
--
-- report_evidence: birebir mesaj şikâyetinde şikâyet edenin kendi
-- cihazından ilettiği fotoğraf; görüntü hattı, tayfasız. Rapora bağlanır
-- (reports.evidence_upload_id); yalnızca platform paneli görür. Şikâyet
-- edenin hesabı silinse de kanıt rapor durdukça kalır (kapaktaki gibi
-- sahipliği kaldırılır).
--
-- reports: dm_message şikâyetinde kanıt metni, fotoğrafı ya da ikisi.
-- channel_message (kanal mesajı şikâyeti; platform sahibi, 2026-11-16):
-- mesaj şikâyet anındaki haliyle saklanır (chat, şikâyet edenin kendi
-- token'ıyla verir): kanal, tür ve metin ya da yer ya da fotoğrafın
-- yükleme kimliği. Yalnızca platform paneli okur. Fotoğrafın kendisi
-- kopyalanmaz; mesaj onu tuttukça panelden açılabilir.
--
-- Geri alma planı: Down sohbet eklerini ve kanıt fotoğraflarını silme
-- kuyruğuna değil doğrudan siler (R2'de kalan nesneler kullanılamaz; temizlik
-- elle), fotoğrafı olup metni olmayan şikâyetleri siler ve kısıtları 00023 /
-- 00013'teki haline döndürür (event_photo korunur). Kanal mesajı
-- şikâyetlerini siler.

-- +goose Up
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'catch_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence'));

-- Görüntülerin iki varyantı, blob'un tek nesnesi, dışa aktarmanın hiçbiri
-- yoktur.
ALTER TABLE uploads DROP CONSTRAINT uploads_variants;
ALTER TABLE uploads ADD CONSTRAINT uploads_variants CHECK (
  (purpose = 'data_export'
    AND preview_type IS NULL AND preview_bytes IS NULL AND full_type IS NULL AND full_bytes IS NULL
    AND width IS NULL AND attached_type IS NULL AND reject_reason IS NULL
    AND (export_bytes IS NULL OR export_bytes BETWEEN 30 AND 16777245))
  OR (purpose = 'dm_attachment'
    AND preview_type IS NULL AND preview_bytes IS NULL
    AND full_type IS NOT NULL AND full_bytes IS NOT NULL
    AND full_type = 'application/octet-stream'
    -- En fazla 10 MiB (ADR-0031).
    AND full_bytes BETWEEN 1 AND 10485760
    AND width IS NULL AND export_bytes IS NULL AND expires_at IS NULL)
  OR (purpose NOT IN ('data_export', 'dm_attachment')
    -- NULL bir CHECK'i geçirir; görüntünün iki varyantı açıkça zorunludur.
    AND preview_type IS NOT NULL AND preview_bytes IS NOT NULL AND full_type IS NOT NULL AND full_bytes IS NOT NULL
    AND preview_type IN ('image/jpeg', 'image/webp') AND full_type IN ('image/jpeg', 'image/webp')
    -- Varyant başına en fazla 10 MiB (ADR-0007).
    AND preview_bytes BETWEEN 1 AND 10485760 AND full_bytes BETWEEN 1 AND 10485760
    AND export_bytes IS NULL AND expires_at IS NULL)
);

ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK (
  (purpose IN ('avatar', 'data_export', 'dm_attachment', 'report_evidence')) = (tayfa_id IS NULL));

-- Hazır görüntünün ölçüleri, hazır dışa aktarmanın boyutu ve süresi vardır;
-- blob'un ölçüsü yoktur.
ALTER TABLE uploads DROP CONSTRAINT uploads_ready;
ALTER TABLE uploads ADD CONSTRAINT uploads_ready CHECK (
  status <> 'ready' OR (ready_at IS NOT NULL AND (
    (purpose = 'data_export' AND export_bytes IS NOT NULL AND expires_at IS NOT NULL)
    OR purpose = 'dm_attachment'
    OR (purpose NOT IN ('data_export', 'dm_attachment') AND width IS NOT NULL)))
);

ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'catch_photo' AND attached_type = 'fishing_catch')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo')
    OR (purpose = 'chat_photo' AND attached_type = 'channel_message')
    OR (purpose = 'dm_attachment' AND attached_type = 'dm_message')
    OR (purpose = 'report_evidence' AND attached_type = 'report'))
);

-- Sahipsiz dosya: tayfanın kapağı ve platformun kanıtı (bağlıyken).
ALTER TABLE uploads DROP CONSTRAINT uploads_owner;
ALTER TABLE uploads ADD CONSTRAINT uploads_owner CHECK (
  user_id IS NOT NULL OR (purpose IN ('cover', 'report_evidence') AND attached_type IS NOT NULL));

-- Görme kararı için core chat'e mesaj kimliğiyle sorar; serbest bırakma
-- olayı da mesaj kimliğiyle gelir (uploads_attached_idx yeterli).

-- Sohbet bağlamasının onayı: chat dosyayı mesajı yazmadan hemen önce,
-- transaction dışında bağlatır. Mesaj hiç yazılmadıysa (geri alma, istemci
-- kopması) bağlama hayalet kalır; onaylanmamış bağlama aynı sahibin yeni
-- mesajına taşınabilir ve uzlaştırma işi 10 dakika sonra chat'e sorar:
-- mesaj dosyayı tutuyorsa onaylar, tutmuyorsa bağlamayı kaldırır (dosya
-- bağlanmamış hazır dosya gibi 7 günde silinir).
ALTER TABLE uploads ADD COLUMN attach_confirmed_at timestamptz;
ALTER TABLE uploads ADD CONSTRAINT uploads_attach_confirmed CHECK (
  attach_confirmed_at IS NULL OR attached_type IN ('channel_message', 'dm_message'));
CREATE INDEX uploads_unconfirmed_chat_idx ON uploads (attached_at)
  WHERE attached_type IN ('channel_message', 'dm_message') AND attach_confirmed_at IS NULL;
GRANT UPDATE (attach_confirmed_at) ON uploads TO core_app;

-- Kanıt fotoğrafı (birebir mesaj şikâyeti).
ALTER TABLE reports ADD COLUMN evidence_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX reports_evidence_upload_id_key ON reports (evidence_upload_id) WHERE evidence_upload_id IS NOT NULL;

ALTER TABLE reports DROP CONSTRAINT reports_evidence;
ALTER TABLE reports ADD CONSTRAINT reports_evidence CHECK (
  (target_type = 'dm_message' OR (evidence_text IS NULL AND evidence_upload_id IS NULL))
  AND (evidence_text IS NULL OR char_length(evidence_text) BETWEEN 1 AND 2000));
-- dm_message şikâyetinin kanıtı oluşturmada en az biridir (metin ya da
-- fotoğraf). Fotoğraf kalıcı silinirse (rapor silinmeden olmaz) kolon
-- SET NULL ile boşalabileceği için "en az biri" INSERT'te denetlenir.
-- +goose StatementBegin
CREATE FUNCTION reports_evidence_required() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.target_type = 'dm_message' AND NEW.evidence_text IS NULL AND NEW.evidence_upload_id IS NULL THEN
    RAISE EXCEPTION 'birebir mesaj şikâyetinin kanıtı yok' USING ERRCODE = 'TF101';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER reports_evidence_required BEFORE INSERT ON reports
  FOR EACH ROW EXECUTE FUNCTION reports_evidence_required();

-- Kanıt fotoğrafı rapora bağlı, hazır ve doğru amaçlı olmalıdır; rapor
-- silinince (1 yıl) dosya silme kuyruğuna girer (00017'deki işlevler).
CREATE TRIGGER reports_evidence_check BEFORE INSERT OR UPDATE OF evidence_upload_id ON reports
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('evidence_upload_id', 'report_evidence', 'report');
CREATE TRIGGER reports_evidence_release AFTER UPDATE OF evidence_upload_id OR DELETE ON reports
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('evidence_upload_id');

-- Kanal mesajı şikâyeti: şikâyet anındaki mesaj (yalnızca platform).
-- message_photo_upload_id'nin yabancı anahtarı yoktur: dosya mesajındır,
-- bağlantı her açılışta mesaja bağlı ve hazır olduğu denetlenerek imzalanır.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message', 'event_photo',
   'channel_message'));
ALTER TABLE reports
  ADD COLUMN channel_id uuid,
  ADD COLUMN message_kind text,
  ADD COLUMN message_body text,
  ADD COLUMN message_photo_upload_id uuid,
  ADD COLUMN place_latitude double precision,
  ADD COLUMN place_longitude double precision,
  ADD COLUMN place_name text;
ALTER TABLE reports ADD CONSTRAINT reports_channel_message CHECK (
  (target_type = 'channel_message') = (channel_id IS NOT NULL)
  AND (target_type = 'channel_message') = (message_kind IS NOT NULL)
  AND (message_kind IS NULL OR message_kind IN ('text', 'photo', 'place'))
  AND (message_kind = 'text') = (message_body IS NOT NULL)
  AND (message_kind = 'photo') = (message_photo_upload_id IS NOT NULL)
  AND (message_kind = 'place') = (place_latitude IS NOT NULL)
  AND (message_kind = 'place') = (place_longitude IS NOT NULL)
  AND (place_name IS NULL OR message_kind = 'place'));
ALTER TABLE reports ADD CONSTRAINT reports_message_body_len CHECK (
  message_body IS NULL OR char_length(message_body) BETWEEN 1 AND 4000);
ALTER TABLE reports ADD CONSTRAINT reports_place CHECK (
  (place_latitude IS NULL OR place_latitude BETWEEN -90 AND 90)
  AND (place_longitude IS NULL OR place_longitude BETWEEN -180 AND 180)
  AND (place_name IS NULL OR char_length(place_name) BETWEEN 1 AND 120));

-- Oluşturma bilgileri değişmez; kanıt fotoğrafı yalnızca boşalabilir
-- (dosyanın satırı silinince ON DELETE SET NULL).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reports_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.tayfa_id IS DISTINCT FROM OLD.tayfa_id
     OR NEW.target_type <> OLD.target_type OR NEW.target_id <> OLD.target_id
     OR NEW.reason <> OLD.reason OR NEW.description IS DISTINCT FROM OLD.description
     OR NEW.evidence_text IS DISTINCT FROM OLD.evidence_text
     OR (NEW.evidence_upload_id IS DISTINCT FROM OLD.evidence_upload_id AND NEW.evidence_upload_id IS NOT NULL)
     OR NEW.channel_id IS DISTINCT FROM OLD.channel_id OR NEW.message_kind IS DISTINCT FROM OLD.message_kind
     OR NEW.message_body IS DISTINCT FROM OLD.message_body
     OR NEW.message_photo_upload_id IS DISTINCT FROM OLD.message_photo_upload_id
     OR NEW.place_latitude IS DISTINCT FROM OLD.place_latitude OR NEW.place_longitude IS DISTINCT FROM OLD.place_longitude
     OR NEW.place_name IS DISTINCT FROM OLD.place_name
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reports_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.tayfa_id IS DISTINCT FROM OLD.tayfa_id
     OR NEW.target_type <> OLD.target_type OR NEW.target_id <> OLD.target_id
     OR NEW.reason <> OLD.reason OR NEW.description IS DISTINCT FROM OLD.description
     OR NEW.evidence_text IS DISTINCT FROM OLD.evidence_text
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
DELETE FROM reports WHERE target_type = 'channel_message';
ALTER TABLE reports DROP CONSTRAINT reports_place, DROP CONSTRAINT reports_message_body_len,
  DROP CONSTRAINT reports_channel_message;
ALTER TABLE reports
  DROP COLUMN place_name, DROP COLUMN place_longitude, DROP COLUMN place_latitude,
  DROP COLUMN message_photo_upload_id, DROP COLUMN message_body, DROP COLUMN message_kind, DROP COLUMN channel_id;
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message', 'event_photo'));
DROP TRIGGER reports_evidence_release ON reports;
DROP TRIGGER reports_evidence_check ON reports;
DROP TRIGGER reports_evidence_required ON reports;
DROP FUNCTION reports_evidence_required();
DELETE FROM reports WHERE target_type = 'dm_message' AND evidence_text IS NULL;
ALTER TABLE reports DROP CONSTRAINT reports_evidence;
ALTER TABLE reports ADD CONSTRAINT reports_evidence CHECK (
  (target_type = 'dm_message') = (evidence_text IS NOT NULL)
  AND (evidence_text IS NULL OR char_length(evidence_text) BETWEEN 1 AND 2000));
DROP INDEX reports_evidence_upload_id_key;
ALTER TABLE reports DROP COLUMN evidence_upload_id;

DELETE FROM uploads WHERE purpose IN ('chat_photo', 'dm_attachment', 'report_evidence');
REVOKE UPDATE (attach_confirmed_at) ON uploads FROM core_app;
DROP INDEX uploads_unconfirmed_chat_idx;
ALTER TABLE uploads DROP CONSTRAINT uploads_attach_confirmed;
ALTER TABLE uploads DROP COLUMN attach_confirmed_at;
ALTER TABLE uploads DROP CONSTRAINT uploads_owner;
ALTER TABLE uploads ADD CONSTRAINT uploads_owner CHECK (user_id IS NOT NULL OR (purpose = 'cover' AND attached_type IS NOT NULL));
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'catch_photo' AND attached_type = 'fishing_catch')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo'))
);
ALTER TABLE uploads DROP CONSTRAINT uploads_ready;
ALTER TABLE uploads ADD CONSTRAINT uploads_ready CHECK (
  status <> 'ready' OR (ready_at IS NOT NULL AND (
    (purpose = 'data_export' AND export_bytes IS NOT NULL AND expires_at IS NOT NULL)
    OR (purpose <> 'data_export' AND width IS NOT NULL)))
);
ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK ((purpose IN ('avatar', 'data_export')) = (tayfa_id IS NULL));
ALTER TABLE uploads DROP CONSTRAINT uploads_variants;
ALTER TABLE uploads ADD CONSTRAINT uploads_variants CHECK (
  (purpose = 'data_export'
    AND preview_type IS NULL AND preview_bytes IS NULL AND full_type IS NULL AND full_bytes IS NULL
    AND width IS NULL AND attached_type IS NULL AND reject_reason IS NULL
    AND (export_bytes IS NULL OR export_bytes BETWEEN 30 AND 16777245))
  OR (purpose <> 'data_export'
    AND preview_type IN ('image/jpeg', 'image/webp') AND full_type IN ('image/jpeg', 'image/webp')
    AND preview_bytes BETWEEN 1 AND 10485760 AND full_bytes BETWEEN 1 AND 10485760
    AND export_bytes IS NULL AND expires_at IS NULL)
);
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover', 'data_export', 'event_photo'));
