-- Ortak kütüphane — ortak eşya (issue #270; ADR-0040 §1; Notion "Faz 3 —
-- Katalog ayrıntıları ve şema ekleri (2026-11-18)" §E.3).
--
--   library_items  tayfada ödünç verilebilen eşya: kitap, kamp malzemesi,
--                  masa oyunu, alet, diğer. Sahibi ekler. `name` 1–80 tek
--                  satır; kitapta ad tayfa sözlüğüne bağlıdır
--                  (`vocabulary_id`, ad alanı `book`, ADR-0037 §4) ve en
--                  fazla 60 karakterdir (sözlük sınırı). `note` ≤ 300
--                  (satır sonu olabilir), en fazla 1 fotoğraf (yükleme
--                  amacı `library_item`). `status`: available / on_loan /
--                  withdrawn. Geri çekme bu tablonun soft delete'idir:
--                  sahibi geri çeker, moderatör ya da platform kaldırır
--                  (`withdrawn_by`), üye ayrılınca kalemleri kendiliğinden
--                  geri çekilir (`withdrawn_by` NULL). Geri çekilen kalemin
--                  notu ve fotoğrafı hemen silinir (dosya silme kuyruğuna
--                  girer); adı ödünç geçmişi için kalır. Geri alma yoktur.
--   library_loans  ödünç: requested → approved → handed_over → returned;
--                  ya da declined / cancelled. `closed`: taraflardan biri
--                  eşya teslim edilmişken tayfadan ayrıldı (`orphaned_at`),
--                  kayıt 30 gün "kapanmamış" kaldı ve kapandı. Kalem başına
--                  aynı anda tek etkin ödünç (requested, approved,
--                  handed_over). `due_at` isteğe bağlıdır, teslimde verilir,
--                  teslimden en fazla 90 gün sonra. Geçince borç alana tek
--                  uygulama içi hatırlatma (`reminded_at`). Para, depozito,
--                  ceza alanı yoktur.
--   library_queue  etkin ödünç varken gelen istekler sıra numarasıyla
--                  bekler; ödünç bitince sıradaki için `requested` ödünç
--                  açılır.
--
-- Modül: `library` tayfa_modules'a eklenir (CHECK genişler; ana sayfa
-- bileşeni `library` bu modüle bağlıdır). Var olan standart tayfalarda açık
-- satır eklenir; anlık tayfada yoktur (kapalı).
--
-- Yükleme amacı `library_item`, bağlama türü `library_item`. Şikâyet hedefi
-- `library_item` (konusu sahibidir).
--
-- Saklama: biten ödünç (ended_at) 1 yıl sonra, geri çekilmiş kalem 30 gün
-- geçmiş ve ödünç kaydı kalmamışsa arka plan işiyle kalıcı silinir.
-- Hesap silinince kişinin kalemleri geri çekilir ve sahibi boşalır, ödünç
-- kayıtlarındaki borç alan boşalır, sıradaki yerleri silinir.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları kaldırır, `library` modül satırlarını,
-- `library_item` hedefli şikâyetleri ve `library_item` amaçlı yüklemeleri
-- siler (kısıtlara sığmadıkları için), CHECK'leri eski haline getirir.
-- Kütüphanedeki kalemler ve ödünç geçmişi kaybolur; fotoğraf dosyaları
-- nesne deposunda sahipsiz kalır (yükleme satırı silindiği için temizlik
-- işi onları göremez). Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE library_items (
  id                   uuid PRIMARY KEY,
  tayfa_id             uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Sahibi; hesap silinince NULL (kalem o zaman geri çekilmiştir).
  owner_id             uuid,
  kind                 text NOT NULL,
  -- Tek satır, kırpılmış, boşluklar tek.
  name                 text NOT NULL,
  -- Yalnızca kitapta: tayfa sözlüğündeki kitap adı.
  vocabulary_id        uuid,
  vocabulary_namespace text,
  -- NULL: not yok. Satır sonu (\n) olabilir; başka denetim karakteri yok.
  note                 text,
  photo_upload_id      uuid REFERENCES uploads (id) ON DELETE SET NULL,
  status               text NOT NULL DEFAULT 'available',
  withdrawn_at         timestamptz,
  -- Kaldıran moderatör ya da platform yöneticisi; sahibi geri çektiyse
  -- sahibi; ayrılmada NULL.
  withdrawn_by         uuid,
  client_request_id    uuid NOT NULL,
  created_at           timestamptz NOT NULL,
  updated_at           timestamptz NOT NULL,

  CONSTRAINT library_items_kind CHECK (kind IN ('book', 'camping_gear', 'board_game', 'tool', 'other')),
  CONSTRAINT library_items_name CHECK (
    char_length(name) BETWEEN 1 AND 80
    AND name = btrim(name)
    AND name !~ '[[:cntrl:]]'
    AND name !~ ' {2}'),
  CONSTRAINT library_items_book CHECK (
    (kind = 'book') = (vocabulary_id IS NOT NULL)
    AND (vocabulary_id IS NULL) = (vocabulary_namespace IS NULL)
    AND (vocabulary_namespace IS NULL OR vocabulary_namespace = 'book')
    AND (kind <> 'book' OR char_length(name) <= 60)),
  CONSTRAINT library_items_note CHECK (
    note IS NULL OR (
      char_length(note) BETWEEN 1 AND 300
      AND note = btrim(note, E' \n')
      AND note !~ '[\u0001-\u0009\u000b-\u001f\u007f-\u009f]'
      AND note !~ ' \n|\n | {2}|\n{3}')),
  CONSTRAINT library_items_status CHECK (status IN ('available', 'on_loan', 'withdrawn')),
  CONSTRAINT library_items_withdrawn CHECK (
    (status = 'withdrawn') = (withdrawn_at IS NOT NULL)
    AND (withdrawn_by IS NULL OR withdrawn_at IS NOT NULL)
    AND (status <> 'withdrawn' OR (note IS NULL AND photo_upload_id IS NULL))),
  CONSTRAINT library_items_owner CHECK (owner_id IS NOT NULL OR status = 'withdrawn'),
  CONSTRAINT library_items_client_request_key UNIQUE (tayfa_id, client_request_id),
  -- Ödünç ve sıra kayıtları kalemin tayfasıyla birlikte bağlanır.
  CONSTRAINT library_items_tayfa_key UNIQUE (id, tayfa_id),
  CONSTRAINT library_items_vocabulary_fkey FOREIGN KEY (vocabulary_id, tayfa_id, vocabulary_namespace)
    REFERENCES tayfa_vocabulary (id, tayfa_id, namespace)
);
CREATE INDEX library_items_tayfa_idx ON library_items (tayfa_id, created_at DESC, id)
  WHERE status <> 'withdrawn';
CREATE INDEX library_items_owner_idx ON library_items (owner_id, tayfa_id) WHERE owner_id IS NOT NULL;
CREATE INDEX library_items_withdrawn_at_idx ON library_items (withdrawn_at) WHERE withdrawn_at IS NOT NULL;
CREATE INDEX library_items_withdrawn_by_idx ON library_items (withdrawn_by) WHERE withdrawn_by IS NOT NULL;
CREATE INDEX library_items_vocabulary_idx ON library_items (vocabulary_id) WHERE vocabulary_id IS NOT NULL;
CREATE UNIQUE INDEX library_items_photo_upload_id_key ON library_items (photo_upload_id)
  WHERE photo_upload_id IS NOT NULL;

CREATE TABLE library_loans (
  id             uuid PRIMARY KEY,
  item_id        uuid NOT NULL,
  tayfa_id       uuid NOT NULL,
  -- Borç alan; hesap silinince NULL.
  borrower_id    uuid,
  state          text NOT NULL,
  requested_at   timestamptz NOT NULL,
  approved_at    timestamptz,
  handed_over_at timestamptz,
  -- İade tarihi: teslimde verilir, teslimden sonra ve en fazla 90 gün.
  due_at         timestamptz,
  -- Süre geçince borç alana giden tek hatırlatmanın zamanı.
  reminded_at    timestamptz,
  -- Eşya teslim edilmişken taraflardan biri tayfadan ayrıldı; kayıt 30 gün
  -- sonra `closed` olur.
  orphaned_at    timestamptz,
  ended_at       timestamptz,

  CONSTRAINT library_loans_item_fkey FOREIGN KEY (item_id, tayfa_id)
    REFERENCES library_items (id, tayfa_id) ON DELETE CASCADE,
  CONSTRAINT library_loans_state CHECK (state IN
    ('requested', 'approved', 'handed_over', 'returned', 'declined', 'cancelled', 'closed')),
  CONSTRAINT library_loans_ended CHECK (
    (state IN ('requested', 'approved', 'handed_over')) = (ended_at IS NULL)),
  CONSTRAINT library_loans_approved CHECK (
    state NOT IN ('approved', 'handed_over', 'returned', 'closed') OR approved_at IS NOT NULL),
  CONSTRAINT library_loans_handed_over CHECK (
    (state IN ('handed_over', 'returned', 'closed')) = (handed_over_at IS NOT NULL)),
  CONSTRAINT library_loans_due CHECK (
    due_at IS NULL OR (handed_over_at IS NOT NULL AND due_at > handed_over_at
                       AND due_at <= handed_over_at + interval '90 days')),
  CONSTRAINT library_loans_reminded CHECK (reminded_at IS NULL OR due_at IS NOT NULL),
  CONSTRAINT library_loans_orphaned CHECK (orphaned_at IS NULL OR handed_over_at IS NOT NULL),
  CONSTRAINT library_loans_closed CHECK (state <> 'closed' OR orphaned_at IS NOT NULL)
);
-- Kalem başına tek etkin ödünç.
CREATE UNIQUE INDEX library_loans_active_key ON library_loans (item_id)
  WHERE state IN ('requested', 'approved', 'handed_over');
CREATE INDEX library_loans_item_idx ON library_loans (item_id, requested_at DESC);
CREATE INDEX library_loans_borrower_idx ON library_loans (borrower_id, tayfa_id) WHERE borrower_id IS NOT NULL;
-- Hatırlatma, kapanma ve saklama işleri.
CREATE INDEX library_loans_due_idx ON library_loans (due_at)
  WHERE state = 'handed_over' AND due_at IS NOT NULL AND reminded_at IS NULL;
CREATE INDEX library_loans_orphaned_idx ON library_loans (orphaned_at)
  WHERE state = 'handed_over' AND orphaned_at IS NOT NULL;
CREATE INDEX library_loans_ended_at_idx ON library_loans (ended_at) WHERE ended_at IS NOT NULL;

CREATE TABLE library_queue (
  id         uuid PRIMARY KEY,
  item_id    uuid NOT NULL,
  tayfa_id   uuid NOT NULL,
  user_id    uuid NOT NULL,
  -- Kalemin sırasındaki numara; küçük olan önce. Boşluk olabilir.
  position   integer NOT NULL,
  created_at timestamptz NOT NULL,

  CONSTRAINT library_queue_item_fkey FOREIGN KEY (item_id, tayfa_id)
    REFERENCES library_items (id, tayfa_id) ON DELETE CASCADE,
  CONSTRAINT library_queue_position CHECK (position >= 1),
  CONSTRAINT library_queue_user_key UNIQUE (item_id, user_id),
  CONSTRAINT library_queue_position_key UNIQUE (item_id, position)
);
CREATE INDEX library_queue_user_idx ON library_queue (user_id, tayfa_id);

-- Kalemin fotoğrafı kaleme bağlı, hazır ve doğru amaçlı olmalıdır; kalem
-- fotoğrafı bıraktığında ya da kalıcı silindiğinde dosya silme kuyruğuna
-- girer (00017).
CREATE TRIGGER library_items_photo_check BEFORE INSERT OR UPDATE OF photo_upload_id ON library_items
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo_upload_id', 'library_item', 'library_item');
CREATE TRIGGER library_items_photo_release AFTER UPDATE OF photo_upload_id OR DELETE ON library_items
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('photo_upload_id');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): kalem
-- başka tayfaya taşınamaz, türü ve istek kimliği değişmez. Ad, not ve
-- fotoğraf düzenlenir; durum ve geri çekme yazılır; hesap silme sahibi
-- boşaltır; arka plan işi kalıcı siler. Ödünç başka kaleme ya da kişiye
-- geçmez: yalnızca durum ve zamanları yazılır, hesap silme borç alanı
-- boşaltır. Sıra yeri eklenir ve silinir. SELECT ve INSERT burada da açıkça
-- verilir.
GRANT SELECT, INSERT, DELETE ON library_items TO core_app;
GRANT UPDATE (owner_id, name, vocabulary_id, vocabulary_namespace, note, photo_upload_id, status, withdrawn_at, withdrawn_by, updated_at)
  ON library_items TO core_app;
GRANT SELECT, INSERT, DELETE ON library_loans TO core_app;
GRANT UPDATE (borrower_id, state, approved_at, handed_over_at, due_at, reminded_at, orphaned_at, ended_at)
  ON library_loans TO core_app;
GRANT SELECT, INSERT, DELETE ON library_queue TO core_app;

-- Modül.
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN ('announcements', 'events', 'qa', 'polls', 'daily', 'library'));

INSERT INTO tayfa_modules (tayfa_id, module, enabled)
SELECT t.id, 'library', true FROM tayfas t WHERE t.kind = 'standard'
ON CONFLICT (tayfa_id, module) DO NOTHING;

-- Yükleme amacı (00038'deki tanımlar, yalnızca library_item eklenir).
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item'));
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

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item', 'library_item'));

-- +goose Down

DELETE FROM reports WHERE target_type = 'library_item';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item'));

DROP TABLE library_queue;
DROP TABLE library_loans;
DROP TABLE library_items;

DELETE FROM uploads WHERE purpose = 'library_item';
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
    OR (purpose = 'report_evidence' AND attached_type = 'report'))
);
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence'));

DELETE FROM tayfa_modules WHERE module = 'library';
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN ('announcements', 'events', 'qa', 'polls', 'daily'));
