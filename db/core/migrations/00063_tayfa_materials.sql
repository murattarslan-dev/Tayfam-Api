-- Materyal kütüphanesi ve engelli alan adı listesi (issue #261; ADR-0040
-- §3; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri (2026-11-18)"
-- §E.3).
--
--   materials        Eğitim tayfalarının ders materyalleri: `link` (yalnızca
--                    https bağlantı) ya da `note_photo` (kişinin kendi not
--                    fotoğrafları, en fazla 3). Dosya/PDF yüklenmez. Başlık
--                    1–80, tek satır. Ders (`subject_key`) zorunlu, konu
--                    (`topic_key`) isteğe bağlı; Tayfam'ın ders-konu
--                    listesinden (catalog_study_tags, 00052) yalnızca anahtar
--                    tutulur, emekliye ayrılan etiket materyalde kalır.
--                    Bağlantıda `url` sunucunun normalleştirdiği adres (en
--                    fazla 500 karakter), `domain` ondan ayrıştırılan alan
--                    adıdır (küçük harf, ASCII; IDN punycode). Sunucu
--                    bağlantıyı hiçbir zaman açmaz: önizleme, başlık, favicon
--                    yoktur. Soft delete: yazarın kendi silmesi fotoğrafları
--                    hemen bırakır (dosyalar silme kuyruğuna girer);
--                    moderatörün ya da platformun kaldırması yalnızca gizler.
--                    Kalıcı silme 30 gün sonra arka plan işiyle.
--   blocked_domains  engelli alan adları: platform panelinden yönetilir
--                    (#262), denetim kaydına yazılır. Alan adı ve bütün alt
--                    alan adları engellidir: yeni bağlantı reddedilir
--                    (`blocked-link`), var olan materyal gizlenir (silinmez;
--                    alan adı listeden çıkınca yeniden görünür). Tek kaynak
--                    `material_domain_blocked(domain)` fonksiyonudur.
--                    Başlangıç satırları (`created_by` NULL): bağlantı
--                    kısaltıcılar (engelli alan adını gizleyebildikleri ve
--                    sunucu bağlantıyı açmadığı için) ve birebir sohbet davet
--                    adresleri (16–17 yaş kullanıcılara tayfa dışı iletişim
--                    yolu açmamak için; ADR-0040 §2'deki "yeni bir iletişim
--                    yolu açılmaz" ilkesi). Notion bu listeyi tanımlamaz;
--                    platform sahibi panelden değiştirir.
--
-- Modül: `materials` tayfa_modules'a eklenir (CHECK genişler; ana sayfa
-- bileşeni `materials` bu modüle bağlıdır). Var olan standart tayfalarda açık
-- satır eklenir; anlık tayfada yoktur (kapalı).
--
-- Yükleme amacı `material_note`, bağlama türü `material`. Şikâyet hedefi
-- `material` (konusu yazarıdır).
--
-- Hesap silinince materyal kalır, yazar ve silen boşalır; kişinin fotoğraf
-- dosyaları silinir (media.UserDeleted).
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları ve fonksiyonu kaldırır, `materials`
-- modül satırlarını, `material` hedefli şikâyetleri ve `material_note`
-- amaçlı yüklemeleri siler (kısıtlara sığmadıkları için), CHECK'leri eski
-- haline getirir. Materyaller ve engelli alan adı listesi kaybolur;
-- fotoğraf dosyaları nesne deposunda sahipsiz kalır (yükleme satırı
-- silindiği için temizlik işi onları göremez). Uygulama kodu da birlikte
-- geri alınmalıdır.

-- +goose Up

CREATE TABLE blocked_domains (
  -- Küçük harf, ASCII (IDN punycode), sonda nokta yok.
  domain     text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- Ekleyen platform yöneticisi; NULL: migration'la gelen başlangıç satırı.
  created_by uuid,

  CONSTRAINT blocked_domains_domain CHECK (
    char_length(domain) BETWEEN 3 AND 253
    AND domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$')
);

-- Alan adı ya da üst alan adlarından biri engelliyse doğru (birincil anahtar
-- üzerinden; NULL alan adı engelli değildir).
-- +goose StatementBegin
CREATE FUNCTION material_domain_blocked(d text) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1 FROM blocked_domains b
    WHERE b.domain IN (
      SELECT array_to_string(p.labels[i:], '.')
      FROM (SELECT string_to_array(d, '.') AS labels) p,
           generate_series(1, cardinality(p.labels)) AS i))
$$;
-- +goose StatementEnd

INSERT INTO blocked_domains (domain) VALUES
  -- Bağlantı kısaltıcılar.
  ('bit.ly'), ('bitly.com'), ('tinyurl.com'), ('t.co'), ('goo.gl'), ('ow.ly'), ('is.gd'), ('v.gd'),
  ('buff.ly'), ('cutt.ly'), ('rebrand.ly'), ('shorturl.at'), ('rb.gy'), ('t.ly'), ('tiny.cc'), ('s.id'),
  ('bl.ink'), ('lnkd.in'), ('qrco.de'),
  -- Birebir sohbet ve grup davet adresleri.
  ('wa.me'), ('chat.whatsapp.com'), ('api.whatsapp.com'), ('t.me'), ('telegram.me'), ('telegram.dog'),
  ('discord.gg'), ('discord.com'), ('discordapp.com'), ('signal.me'), ('signal.group');

CREATE TABLE materials (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Yazar; hesap silinince NULL.
  author_id         uuid,
  kind              text NOT NULL,
  -- Tek satır, kırpılmış, boşluklar tek.
  title             text NOT NULL,
  -- Yalnızca `link`: normalleştirilmiş https adresi ve alan adı.
  url               text,
  domain            text,
  -- Yalnızca `note_photo`: yazarın kendi fotoğrafları, sırasıyla.
  photo1_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  photo2_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  photo3_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  -- Ders ve konu anahtarı (catalog_study_tags; anahtar değişmez, satır
  -- silinmez, emekliye ayrılır). Seçimi catalog.CheckStudySelection
  -- doğrular; bu kısıtlar ikinci savunma hattıdır.
  subject_key       text NOT NULL REFERENCES catalog_study_tags (key),
  topic_key         text REFERENCES catalog_study_tags (key),
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL,
  deleted_at        timestamptz,
  -- Silen yazar, moderatör ya da platform yöneticisi.
  deleted_by        uuid,

  CONSTRAINT materials_kind CHECK (kind IN ('link', 'note_photo')),
  CONSTRAINT materials_title CHECK (
    char_length(title) BETWEEN 1 AND 80
    AND title = btrim(title)
    AND title !~ '[[:cntrl:]]'
    AND title !~ ' {2}'),
  CONSTRAINT materials_link CHECK (
    (kind = 'link') = (url IS NOT NULL)
    AND (url IS NULL) = (domain IS NULL)
    AND (url IS NULL OR (
      char_length(url) <= 500
      AND url LIKE 'https://%'
      AND url !~ '[[:space:][:cntrl:]\\]'))
    AND (domain IS NULL OR (
      char_length(domain) <= 253
      AND domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'))),
  CONSTRAINT materials_photos CHECK (
    (kind = 'note_photo' OR (photo1_upload_id IS NULL AND photo2_upload_id IS NULL AND photo3_upload_id IS NULL))
    AND (photo1_upload_id IS NULL OR photo2_upload_id IS NULL OR photo1_upload_id <> photo2_upload_id)
    AND (photo1_upload_id IS NULL OR photo3_upload_id IS NULL OR photo1_upload_id <> photo3_upload_id)
    AND (photo2_upload_id IS NULL OR photo3_upload_id IS NULL OR photo2_upload_id <> photo3_upload_id)),
  CONSTRAINT materials_deleted CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT materials_client_request_key UNIQUE (tayfa_id, client_request_id)
);
CREATE INDEX materials_tayfa_recent_idx ON materials (tayfa_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX materials_subject_idx ON materials (tayfa_id, subject_key, topic_key) WHERE deleted_at IS NULL;
CREATE INDEX materials_author_idx ON materials (author_id, tayfa_id) WHERE author_id IS NOT NULL;
CREATE INDEX materials_deleted_by_idx ON materials (deleted_by) WHERE deleted_by IS NOT NULL;
CREATE INDEX materials_deleted_at_idx ON materials (deleted_at) WHERE deleted_at IS NOT NULL;
-- Yükleme kalıcı silinince fotoğraf kolonu boşalır (ON DELETE SET NULL).
CREATE UNIQUE INDEX materials_photo1_upload_id_key ON materials (photo1_upload_id) WHERE photo1_upload_id IS NOT NULL;
CREATE UNIQUE INDEX materials_photo2_upload_id_key ON materials (photo2_upload_id) WHERE photo2_upload_id IS NOT NULL;
CREATE UNIQUE INDEX materials_photo3_upload_id_key ON materials (photo3_upload_id) WHERE photo3_upload_id IS NOT NULL;

-- Fotoğraf materyale bağlı, hazır ve doğru amaçlı olmalıdır (00017).
CREATE TRIGGER materials_photo1_check BEFORE INSERT OR UPDATE OF photo1_upload_id ON materials
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo1_upload_id', 'material_note', 'material');
CREATE TRIGGER materials_photo2_check BEFORE INSERT OR UPDATE OF photo2_upload_id ON materials
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo2_upload_id', 'material_note', 'material');
CREATE TRIGGER materials_photo3_check BEFORE INSERT OR UPDATE OF photo3_upload_id ON materials
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo3_upload_id', 'material_note', 'material');

-- Materyal bir fotoğrafı bıraktığında ya da kalıcı silindiğinde dosya
-- silme kuyruğuna girer. Kolon başına uploads_release_ref yetmez: düzenleme
-- fotoğrafların sırasını değiştirebilir, bir kolondan çıkan fotoğraf başka
-- kolonda kalabilir.
-- +goose StatementBegin
CREATE FUNCTION materials_release_photos() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  kept uuid[] := '{}';
BEGIN
  IF TG_OP = 'UPDATE' THEN
    kept := array_remove(ARRAY[NEW.photo1_upload_id, NEW.photo2_upload_id, NEW.photo3_upload_id], NULL);
  END IF;
  UPDATE uploads SET status = 'deleting'
  WHERE id IN (OLD.photo1_upload_id, OLD.photo2_upload_id, OLD.photo3_upload_id)
    AND NOT (id = ANY (kept))
    AND status <> 'deleting';
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER materials_photos_release
  AFTER UPDATE OF photo1_upload_id, photo2_upload_id, photo3_upload_id OR DELETE ON materials
  FOR EACH ROW EXECUTE FUNCTION materials_release_photos();

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016):
-- materyal başka tayfaya taşınamaz, türü ve istek kimliği değişmez; başlık,
-- bağlantı, fotoğraflar ve etiketler düzenlenir; silme yazılır; hesap silme
-- yazarı ve sileni boşaltır; arka plan işi kalıcı siler. Engelli alan adı
-- listesini servis yalnızca okur (yönetimi platform panelinin, #262).
GRANT SELECT, INSERT, DELETE ON materials TO core_app;
GRANT UPDATE (author_id, title, url, domain, photo1_upload_id, photo2_upload_id, photo3_upload_id,
              subject_key, topic_key, updated_at, deleted_at, deleted_by)
  ON materials TO core_app;
REVOKE INSERT ON blocked_domains FROM core_app;
GRANT SELECT ON blocked_domains TO core_app;

-- Modül.
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library', 'question_pool', 'materials'));
INSERT INTO tayfa_modules (tayfa_id, module, enabled)
SELECT t.id, 'materials', true FROM tayfas t WHERE t.kind = 'standard'
ON CONFLICT (tayfa_id, module) DO NOTHING;

-- Yükleme amacı (00062'deki tanımlar, yalnızca material_note eklenir).
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item', 'question_pool', 'material_note'));
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
    OR (purpose = 'question_pool' AND attached_type IN ('question', 'answer'))
    OR (purpose = 'material_note' AND attached_type = 'material'))
);

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item', 'library_item', 'material'));

-- +goose Down

DELETE FROM reports WHERE target_type = 'material';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item', 'library_item'));

DROP TABLE materials;
DROP FUNCTION materials_release_photos();
DROP FUNCTION material_domain_blocked(text);
DROP TABLE blocked_domains;

DELETE FROM uploads WHERE purpose = 'material_note';
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
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item', 'question_pool'));

DELETE FROM tayfa_modules WHERE module = 'materials';
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library', 'question_pool'));
