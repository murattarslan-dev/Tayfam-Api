-- Soru toplama bileşeni (`questions`; issue #256; ADR-0025 §4, §5, §11).
--
--   event_questions       söyleşide katılımcıların sorduğu sorular. Soru
--                         anonim olabilir (bileşenin `anonymous`
--                         parametresi açıksa): yazar yalnızca sunucuda
--                         tutulur; istemciye dönen hiçbir yanıtta, tayfa
--                         yönetimine de, yer almaz. Organizatör soruyu
--                         "cevaplandı" işaretler. İçerik tablosudur: soft
--                         delete, kalıcı silme 30 gün sonra arka plan
--                         işiyle.
--   event_question_votes  sorulara verilen oylar: kişi başına soru başına
--                         bir oy. Oy sayısı saklanmaz, okunurken sayılır.
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('questions'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Şikâyet: yeni hedef türü `event_question`. Anonim soru şikâyet edilirse
-- yazar şikâyet kaydında (subject_user_id) tutulur ve yalnızca platform
-- yönetimine görünür (ADR-0025 §5, ADR-0012): reports.subject_hidden
-- işaretli şikâyette tayfa kuyruğu yazarı boş okur. İşaret şikâyet açılırken
-- konur ve değişmez.
--
-- Kişisel veri (ADR-0025 §11): soru ve oy kişiye aittir, dışa aktarmaya
-- girer (anonim sorular dahil) ve hesap silmede silinir; cevaplandı
-- işaretleyen ve silen boşalır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde soru
-- toplama bileşeni yok).
--
-- Geri alma planı: Down tabloları ve şikâyet işaretini kaldırır. Sorular ve
-- oylar kaybolur; `event_question` hedefli şikâyetler de silinir (hedef türü
-- kısıtına sığmadıkları için). Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_questions (
  id                uuid PRIMARY KEY,
  event_id          uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'questions').
  component_key     text NOT NULL,
  -- Soran. Anonim soruda da tutulur (yalnızca sunucuda). Hesap silinince
  -- soruları silinir.
  author_id         uuid NOT NULL,
  anonymous         boolean NOT NULL,
  -- Kırpılmış, iç boşlukları teke indirilmiş metin; denetim karakteri yok.
  body              text NOT NULL,
  answered_at       timestamptz,
  -- Cevaplandı işaretleyen yönetici. Hesap silinince boşaltılır.
  answered_by       uuid,
  client_request_id uuid NOT NULL,
  deleted_at        timestamptz,
  deleted_by        uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_questions_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_questions_body CHECK (
    char_length(body) BETWEEN 1 AND 500 AND body = btrim(body) AND body !~ '[[:cntrl:]]'),
  CONSTRAINT event_questions_answered_by CHECK (answered_by IS NULL OR answered_at IS NOT NULL),
  CONSTRAINT event_questions_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT event_questions_client_request_key UNIQUE (author_id, client_request_id),
  -- Oyların bileşik foreign key'i için.
  CONSTRAINT event_questions_scope_key UNIQUE (id, event_id, component_key)
);
CREATE INDEX event_questions_event_idx ON event_questions (event_id, component_key, created_at, id)
  WHERE deleted_at IS NULL;
CREATE INDEX event_questions_deleted_at_idx ON event_questions (deleted_at) WHERE deleted_at IS NOT NULL;
-- Kişi başı sınır, hesap silme ve dışa aktarma.
CREATE INDEX event_questions_author_idx ON event_questions (author_id, event_id, component_key);
CREATE INDEX event_questions_answered_by_idx ON event_questions (answered_by) WHERE answered_by IS NOT NULL;
CREATE INDEX event_questions_deleted_by_idx ON event_questions (deleted_by) WHERE deleted_by IS NOT NULL;
CREATE TRIGGER event_questions_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_questions
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('questions');

CREATE TABLE event_question_votes (
  question_id   uuid NOT NULL,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  -- Oy veren. Hesap silinince oyları silinir.
  user_id       uuid NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (question_id, user_id),
  CONSTRAINT event_question_votes_question FOREIGN KEY (question_id, event_id, component_key)
    REFERENCES event_questions (id, event_id, component_key) ON DELETE CASCADE
);
-- Dışa aktarma ve hesap silme.
CREATE INDEX event_question_votes_user_id_idx ON event_question_votes (user_id);
CREATE TRIGGER event_question_votes_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_question_votes
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('questions');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): soru
-- başka etkinliğe, bileşene ya da kişiye taşınamaz; metni ve anonimliği
-- değişmez. Yalnızca cevaplandı işareti ve soft delete yazılır; arka plan
-- işi ve hesap silme kalıcı siler. Oy yazılır ve silinir, değişmez. SELECT
-- ve INSERT burada da açıkça verilir.
GRANT SELECT, INSERT, DELETE ON event_questions TO core_app;
GRANT UPDATE (answered_at, answered_by, deleted_at, deleted_by) ON event_questions TO core_app;
GRANT SELECT, INSERT, DELETE ON event_question_votes TO core_app;

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question'));

-- Yazarı tayfa yönetiminden gizlenen şikâyet (anonim soru).
ALTER TABLE reports ADD COLUMN subject_hidden boolean NOT NULL DEFAULT false;
ALTER TABLE reports ADD CONSTRAINT reports_subject_hidden CHECK (NOT subject_hidden OR target_type = 'event_question');

-- +goose StatementBegin
CREATE FUNCTION reports_subject_hidden_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.subject_hidden IS DISTINCT FROM OLD.subject_hidden THEN
    RAISE EXCEPTION 'şikâyetin yazar gizliliği değişmez' USING ERRCODE = 'TF100';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER reports_subject_hidden_guard BEFORE UPDATE OF subject_hidden ON reports
  FOR EACH ROW EXECUTE FUNCTION reports_subject_hidden_guard();

-- +goose Down

DROP TRIGGER reports_subject_hidden_guard ON reports;
DROP FUNCTION reports_subject_hidden_guard();
ALTER TABLE reports DROP CONSTRAINT reports_subject_hidden;
ALTER TABLE reports DROP COLUMN subject_hidden;
DELETE FROM reports WHERE target_type = 'event_question';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message'));

DROP TABLE event_question_votes;
DROP TABLE event_questions;
