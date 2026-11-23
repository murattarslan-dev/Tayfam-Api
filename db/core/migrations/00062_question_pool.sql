-- Soru havuzu (issue #271; ADR-0040 §2; ADR-0037 §3 `pool_link`; Notion
-- "Faz 3 — Katalog ayrıntıları ve şema ekleri (2026-11-18)" §E.3).
--
-- Eğitim tayfalarının soru havuzu, Soru-Cevap tablolarının üstündedir:
-- `questions.pool` ('qa' / 'question_pool') iki modülü ayırır; cevaplar,
-- faydalı oylar, soft delete, geri alma ve şikâyet aynı altyapıdır.
-- "Çözüldü" işareti yoktur; cevaplar faydalı oyuyla sıralanır.
--
--   questions  `pool`; havuz sorusunda ders (`subject_key`, zorunlu) ve
--              konu (`topic_key`, isteğe bağlı) Tayfam'ın ders-konu
--              listesinden (catalog_study_tags, 00052) anahtarla tutulur;
--              etiket emekliye ayrılsa da soru onu göstermeye devam eder.
--              Havuz sorusunun başlığı yoktur (''), metin `body`'dedir; en
--              fazla 2 fotoğraf (`photo1_upload_id`, `photo2_upload_id`;
--              kişinin kendi not/soru fotoğrafı, ADR-0020). Metin ya da
--              fotoğraftan en az biri uygulamada zorunludur (fotoğraf hesap
--              silinince dosyasıyla düşebildiği için veritabanında değil).
--              Serbest etiket (tags) havuzda kullanılmaz.
--   answers    `pool` sorunun havuzudur: (question_id, tayfa_id, pool)
--              bileşik foreign key'i ile soruya bağlanır, başka havuzun
--              sorusuna cevap yazılamaz. Havuz cevabı metin ve/veya en fazla
--              2 fotoğraftır; Soru-Cevap cevabında metin zorunlu kalır,
--              fotoğraf olmaz.
--   event_pool_links  `pool_link` bileşeni: etkinliği tayfanın havuzundaki
--              sorulara bağlar (en fazla 50, tür tanımındaki `max_links`).
--              Satır (event_id, component_key) ile bileşene bağlıdır
--              (event_component_guard('pool_link')); soru aynı tayfanın
--              havuz sorusu olmalıdır (tetikleyici). Soru kalıcı silinince
--              bağı da silinir. Satır kişi kimliği taşımaz.
--
-- Modül: `question_pool` tayfa_modules'a eklenir (CHECK genişler; ana sayfa
-- bileşeni `question_pool` bu modüle bağlıdır). Var olan standart
-- tayfalarda açık satır eklenir; anlık tayfada yoktur (kapalı).
--
-- Yükleme amacı `question_pool`; bağlama türleri `question` ve `answer`.
-- Şikâyet hedefleri bugünkü `question` / `answer`'dır (modülü sorunun
-- havuzundan okunur). Şikâyet nedenlerine `paid_tutoring` (ücretli özel
-- ders teklifi/reklamı) eklenir.
--
-- Veri taşınmaz ve silinmez: var olan sorular ve cevaplar `qa` havuzunda
-- kalır; katalog değişmez.
--
-- Geri alma planı: Down havuz sorularını (cevapları, oyları ve etkinlik
-- bağlarıyla birlikte; CASCADE) ve onlara yazılmış şikâyetleri siler,
-- `paid_tutoring` şikâyetlerinin nedenini `other` yapar, `question_pool`
-- amaçlı yüklemeleri ve modül satırlarını siler, kolonları ve kısıtları
-- eski haline getirir. Havuzdaki sorular ve cevaplar kaybolur; fotoğraf
-- dosyaları nesne deposunda sahipsiz kalır (yükleme satırı silindiği için
-- temizlik işi onları göremez). Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library', 'question_pool'));
INSERT INTO tayfa_modules (tayfa_id, module, enabled)
SELECT t.id, 'question_pool', true FROM tayfas t WHERE t.kind = 'standard'
ON CONFLICT (tayfa_id, module) DO NOTHING;

ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item', 'question_pool'));
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'log_photo' AND attached_type = 'log_entry')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo')
    OR (purpose = 'chat_photo' AND attached_type = 'channel_message')
    OR (purpose = 'dm_attachment' AND attached_type = 'dm_message')
    OR (purpose = 'report_evidence' AND attached_type = 'report')
    OR (purpose = 'library_item' AND attached_type = 'library_item')
    OR (purpose = 'question_pool' AND attached_type IN ('question', 'answer')))
);

ALTER TABLE reports DROP CONSTRAINT reports_reason;
ALTER TABLE reports ADD CONSTRAINT reports_reason CHECK (reason IN (
  'harassment', 'hate_speech', 'violence', 'privacy', 'impersonation',
  'illegal_trade', 'drugs', 'gambling', 'dangerous_activity', 'fake_event',
  'sexual_content', 'child_safety', 'spam', 'copyright', 'staff_conduct', 'other',
  'paid_tutoring'));

-- Sorular. Var olanlar 'qa' havuzundadır.
ALTER TABLE questions
  ADD COLUMN pool             text NOT NULL DEFAULT 'qa',
  -- Ders ve konu anahtarı (catalog_study_tags; anahtar değişmez, satır
  -- silinmez, emekliye ayrılır). Seçimi catalog.CheckStudySelection
  -- doğrular; bu kısıtlar ikinci savunma hattıdır.
  ADD COLUMN subject_key      text REFERENCES catalog_study_tags (key),
  ADD COLUMN topic_key        text REFERENCES catalog_study_tags (key),
  ADD COLUMN photo1_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL,
  ADD COLUMN photo2_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
ALTER TABLE questions ADD CONSTRAINT questions_pool CHECK (pool IN ('qa', 'question_pool'));
ALTER TABLE questions ADD CONSTRAINT questions_study CHECK (
  (pool = 'question_pool') = (subject_key IS NOT NULL)
  AND (topic_key IS NULL OR subject_key IS NOT NULL)
);
-- Fotoğraf yalnızca havuzda; aynı dosya iki kez yok. (Biri hesap silinince
-- düşebilir: ikinci fotoğraf tek başına kalabilir.)
ALTER TABLE questions ADD CONSTRAINT questions_photos CHECK (
  (pool = 'question_pool' OR (photo1_upload_id IS NULL AND photo2_upload_id IS NULL))
  AND (photo1_upload_id IS NULL OR photo2_upload_id IS NULL OR photo1_upload_id <> photo2_upload_id)
);
-- Soru-Cevap sorusunun başlığı 10–150 karakterdir (yazarın kendi sildiği
-- boşaltılmıştır); havuz sorusunun başlığı yoktur.
ALTER TABLE questions DROP CONSTRAINT questions_title_len;
ALTER TABLE questions ADD CONSTRAINT questions_title_len CHECK (
  char_length(title) <= 150
  AND (pool = 'qa' OR title = '')
  AND (deleted_at IS NOT NULL OR pool = 'question_pool' OR char_length(title) >= 10)
);
-- answers'ın havuzlu bileşik foreign key'i için.
ALTER TABLE questions ADD CONSTRAINT questions_id_tayfa_pool_key UNIQUE (id, tayfa_id, pool);
-- Havuza göre son sorular; ders filtresi.
DROP INDEX questions_tayfa_recent_idx;
CREATE INDEX questions_tayfa_recent_idx ON questions (tayfa_id, pool, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX questions_subject_idx ON questions (tayfa_id, subject_key, topic_key)
  WHERE pool = 'question_pool' AND deleted_at IS NULL;
CREATE TRIGGER questions_photo1_check BEFORE INSERT OR UPDATE OF photo1_upload_id ON questions
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo1_upload_id', 'question_pool', 'question');
CREATE TRIGGER questions_photo2_check BEFORE INSERT OR UPDATE OF photo2_upload_id ON questions
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo2_upload_id', 'question_pool', 'question');
CREATE TRIGGER questions_photo1_release AFTER UPDATE OF photo1_upload_id OR DELETE ON questions
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo1_upload_id');
CREATE TRIGGER questions_photo2_release AFTER UPDATE OF photo2_upload_id OR DELETE ON questions
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo2_upload_id');

-- Cevaplar: havuz sorunun havuzudur.
ALTER TABLE answers
  ADD COLUMN pool             text NOT NULL DEFAULT 'qa',
  ADD COLUMN photo1_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL,
  ADD COLUMN photo2_upload_id uuid REFERENCES uploads (id) ON DELETE SET NULL;
ALTER TABLE answers DROP CONSTRAINT answers_question_fkey;
ALTER TABLE answers ADD CONSTRAINT answers_question_fkey FOREIGN KEY (question_id, tayfa_id, pool)
  REFERENCES questions (id, tayfa_id, pool) ON DELETE CASCADE;
ALTER TABLE answers ADD CONSTRAINT answers_photos CHECK (
  (pool = 'question_pool' OR (photo1_upload_id IS NULL AND photo2_upload_id IS NULL))
  AND (photo1_upload_id IS NULL OR photo2_upload_id IS NULL OR photo1_upload_id <> photo2_upload_id)
);
-- Soru-Cevap cevabında metin zorunludur; havuz cevabı yalnızca fotoğraf
-- olabilir (uygulama metin ya da fotoğraf ister).
ALTER TABLE answers DROP CONSTRAINT answers_body_len;
ALTER TABLE answers ADD CONSTRAINT answers_body_len CHECK (
  char_length(body) <= 10000 AND (deleted_at IS NOT NULL OR pool = 'question_pool' OR char_length(body) >= 1)
);
CREATE TRIGGER answers_photo1_check BEFORE INSERT OR UPDATE OF photo1_upload_id ON answers
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo1_upload_id', 'question_pool', 'answer');
CREATE TRIGGER answers_photo2_check BEFORE INSERT OR UPDATE OF photo2_upload_id ON answers
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo2_upload_id', 'question_pool', 'answer');
CREATE TRIGGER answers_photo1_release AFTER UPDATE OF photo1_upload_id OR DELETE ON answers
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo1_upload_id');
CREATE TRIGGER answers_photo2_release AFTER UPDATE OF photo2_upload_id OR DELETE ON answers
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo2_upload_id');

-- `pool_link` bileşeni.
CREATE TABLE event_pool_links (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'pool_link').
  component_key text NOT NULL,
  question_id   uuid NOT NULL REFERENCES questions (id) ON DELETE CASCADE,
  -- Organizatörün verdiği sıra (0'dan).
  position      smallint NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key, question_id),
  CONSTRAINT event_pool_links_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_pool_links_position CHECK (position BETWEEN 0 AND 49)
);
CREATE UNIQUE INDEX event_pool_links_position_key ON event_pool_links (event_id, component_key, position);
CREATE INDEX event_pool_links_question_idx ON event_pool_links (question_id);
CREATE TRIGGER event_pool_links_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_pool_links
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('pool_link');

-- Bağlanan soru etkinliğin tayfasının havuz sorusudur (IDOR'a karşı ikinci
-- savunma hattı; events'te (id, tayfa_id) anahtarı olmadığı için
-- tetikleyici).
-- +goose StatementBegin
CREATE FUNCTION event_pool_links_question_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM questions q JOIN events e ON e.tayfa_id = q.tayfa_id
    WHERE q.id = NEW.question_id AND e.id = NEW.event_id AND q.pool = 'question_pool'
  ) THEN
    RAISE EXCEPTION 'soru etkinliğin tayfasının soru havuzunda değil'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_pool_links_question';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_pool_links_question_check BEFORE INSERT OR UPDATE OF event_id, question_id ON event_pool_links
  FOR EACH ROW EXECUTE FUNCTION event_pool_links_question_check();

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): liste
-- bütünüyle değiştirilir (sil + ekle); satır taşınmaz.
GRANT DELETE ON event_pool_links TO core_app;

-- +goose Down

DROP TABLE event_pool_links;
DROP FUNCTION event_pool_links_question_check();

DELETE FROM reports r WHERE (r.target_type = 'question' AND EXISTS (
    SELECT 1 FROM questions q WHERE q.id = r.target_id AND q.pool = 'question_pool'))
  OR (r.target_type = 'answer' AND EXISTS (
    SELECT 1 FROM answers a WHERE a.id = r.target_id AND a.pool = 'question_pool'));
DELETE FROM questions WHERE pool = 'question_pool';
UPDATE reports SET reason = 'other' WHERE reason = 'paid_tutoring';

DROP TRIGGER answers_photo1_check ON answers;
DROP TRIGGER answers_photo2_check ON answers;
DROP TRIGGER answers_photo1_release ON answers;
DROP TRIGGER answers_photo2_release ON answers;
ALTER TABLE answers DROP CONSTRAINT answers_body_len;
ALTER TABLE answers ADD CONSTRAINT answers_body_len CHECK (
  char_length(body) <= 10000 AND (deleted_at IS NOT NULL OR char_length(body) >= 1)
);
ALTER TABLE answers DROP CONSTRAINT answers_question_fkey;
ALTER TABLE answers DROP CONSTRAINT answers_photos;
ALTER TABLE answers DROP COLUMN photo1_upload_id, DROP COLUMN photo2_upload_id, DROP COLUMN pool;
ALTER TABLE answers ADD CONSTRAINT answers_question_fkey FOREIGN KEY (question_id, tayfa_id)
  REFERENCES questions (id, tayfa_id) ON DELETE CASCADE;

DROP TRIGGER questions_photo1_check ON questions;
DROP TRIGGER questions_photo2_check ON questions;
DROP TRIGGER questions_photo1_release ON questions;
DROP TRIGGER questions_photo2_release ON questions;
DROP INDEX questions_subject_idx;
DROP INDEX questions_tayfa_recent_idx;
CREATE INDEX questions_tayfa_recent_idx ON questions (tayfa_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
ALTER TABLE questions DROP CONSTRAINT questions_id_tayfa_pool_key;
ALTER TABLE questions DROP CONSTRAINT questions_title_len;
ALTER TABLE questions DROP CONSTRAINT questions_photos;
ALTER TABLE questions DROP CONSTRAINT questions_study;
ALTER TABLE questions DROP CONSTRAINT questions_pool;
ALTER TABLE questions DROP COLUMN photo1_upload_id, DROP COLUMN photo2_upload_id,
  DROP COLUMN topic_key, DROP COLUMN subject_key, DROP COLUMN pool;
ALTER TABLE questions ADD CONSTRAINT questions_title_len CHECK (
  char_length(title) <= 150 AND (deleted_at IS NOT NULL OR char_length(title) >= 10)
);

ALTER TABLE reports DROP CONSTRAINT reports_reason;
ALTER TABLE reports ADD CONSTRAINT reports_reason CHECK (reason IN (
  'harassment', 'hate_speech', 'violence', 'privacy', 'impersonation',
  'illegal_trade', 'drugs', 'gambling', 'dangerous_activity', 'fake_event',
  'sexual_content', 'child_safety', 'spam', 'copyright', 'staff_conduct', 'other'));

DELETE FROM uploads WHERE purpose = 'question_pool';
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'log_photo' AND attached_type = 'log_entry')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event'))
    OR (purpose = 'event_photo' AND attached_type = 'event_photo')
    OR (purpose = 'chat_photo' AND attached_type = 'channel_message')
    OR (purpose = 'dm_attachment' AND attached_type = 'dm_message')
    OR (purpose = 'report_evidence' AND attached_type = 'report')
    OR (purpose = 'library_item' AND attached_type = 'library_item'))
);
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item'));

DELETE FROM tayfa_modules WHERE module = 'question_pool';
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library'));
