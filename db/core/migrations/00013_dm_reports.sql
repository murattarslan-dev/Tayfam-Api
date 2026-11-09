-- Birebir mesaj şikâyeti (issue #95 madde 7; ADR-0005 "Şikâyet", ADR-0009;
-- platform sahibinin 2026-11-09 kararı: seçenek (a)).
--
-- Birebir mesaj bir tayfaya ait değildir. Bu yüzden:
--   - target_type = 'dm_message' olan şikâyette tayfa_id NULL'dır; başka her
--     hedefte doludur (reports_tayfa_dm). Tayfa kuyrukları tayfa_id ile
--     sorgulandığı ve bu şikâyetler platform_only olduğu için hiçbir tayfa
--     kuyruğunda görünmez; oluşturuldukları anda platforma yükselir
--     (escalation_reason = 'direct_message').
--   - target_id mesajın kimliği (chat'in verisi), subject_user_id
--     göndericidir. core ikisini chat'e şikâyet edenin kendi token'ıyla
--     sorar (DirectMessageService.VerifyDirectMessageForReport): şikâyet
--     eden konuşmanın tarafı mı, mesaj var mı, göndereni kim. İçerik
--     chat'ten gelmez.
--   - evidence_text şikâyet edenin kendi cihazından bilinçli olarak
--     ilettiği açık metindir (1–2000 karakter; E2E aşamasında sunucu
--     içeriği göremeyeceği için tek kanıt yolu, ADR-0005). Yalnızca DM
--     şikâyetinde bulunur ve zorunludur. Log'a ve olaylara yazılmaz;
--     yalnızca platform paneli okur. Oluşturma bilgisi olduğu için
--     değişmez (reports_guard). Çözülmüş şikâyetle birlikte 1 yıl sonra
--     silinir (mevcut saklama işi).
--   - staff_conduct DM'de anlamsızdır (tayfa yetkilisi yok).
--
-- reports_guard: tayfa_id artık NULL olabildiği için karşılaştırması
-- IS DISTINCT FROM'a çevrilir (NULL'dan bir değere geçiş de reddedilir);
-- evidence_text de değişmez.
--
-- Geri alma planı: Down DM şikâyetlerini siler (yalnızca bu migration'la
-- oluşabilirler), kısıtları ve tetikleyiciyi 00011'deki haline döndürür.

-- +goose Up
ALTER TABLE reports
  ALTER COLUMN tayfa_id DROP NOT NULL,
  ADD COLUMN evidence_text text;

ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message'));
ALTER TABLE reports ADD CONSTRAINT reports_tayfa_dm CHECK ((tayfa_id IS NULL) = (target_type = 'dm_message'));
ALTER TABLE reports ADD CONSTRAINT reports_evidence CHECK (
  (target_type = 'dm_message') = (evidence_text IS NOT NULL)
  AND (evidence_text IS NULL OR char_length(evidence_text) BETWEEN 1 AND 2000));
ALTER TABLE reports ADD CONSTRAINT reports_dm_reason CHECK (target_type <> 'dm_message' OR reason <> 'staff_conduct');

ALTER TABLE reports DROP CONSTRAINT reports_platform_only;
ALTER TABLE reports ADD CONSTRAINT reports_platform_only CHECK (
  platform_only = (target_type IN ('tayfa', 'dm_message') OR reason = 'staff_conduct'));

ALTER TABLE reports DROP CONSTRAINT reports_escalation_reason;
ALTER TABLE reports ADD CONSTRAINT reports_escalation_reason CHECK (escalation_reason IS NULL OR escalation_reason IN
  ('tayfa_target', 'staff_conduct', 'child_safety', 'minor_involved', 'unresolved', 'direct_message'));

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

-- +goose Down
DELETE FROM reports WHERE target_type = 'dm_message';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reports_guard() RETURNS trigger
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

ALTER TABLE reports DROP CONSTRAINT reports_escalation_reason;
ALTER TABLE reports ADD CONSTRAINT reports_escalation_reason CHECK (escalation_reason IS NULL OR escalation_reason IN
  ('tayfa_target', 'staff_conduct', 'child_safety', 'minor_involved', 'unresolved'));
ALTER TABLE reports DROP CONSTRAINT reports_platform_only;
ALTER TABLE reports ADD CONSTRAINT reports_platform_only CHECK (platform_only = (target_type = 'tayfa' OR reason = 'staff_conduct'));
ALTER TABLE reports DROP CONSTRAINT reports_dm_reason;
ALTER TABLE reports DROP CONSTRAINT reports_evidence;
ALTER TABLE reports DROP CONSTRAINT reports_tayfa_dm;
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch'));
ALTER TABLE reports
  DROP COLUMN evidence_text,
  ALTER COLUMN tayfa_id SET NOT NULL;
