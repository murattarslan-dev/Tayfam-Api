-- Üye garajı (issue #272; ADR-0040 §4; Notion "Faz 3 — Katalog ayrıntıları
-- ve şema ekleri (2026-11-18)" §E.3).
--
--   garage_vehicles          kişinin garajındaki araçlar. Garaj kişiye
--                            aittir ve tektir (profil); tayfaya bağlı
--                            değildir. Marka 1–40, model ≤ 40, model yılı,
--                            renk ≤ 30, kısa not ≤ 200 (hepsi tek satır), en
--                            fazla 3 fotoğraf. PLAKA ALANI YOKTUR: kalıcı
--                            profil verisinde plaka kişiyi araca ve konuma
--                            bağlayan gereksiz bir veridir. Kişi başına en
--                            fazla 10 araç (uygulama denetler). Satılık /
--                            ilan alanı yoktur. Soft delete: sahibinin kendi
--                            silmesi fotoğrafları hemen bırakır;
--                            moderatörün ya da platformun kaldırması yalnızca
--                            gizler. Kalıcı silme 30 gün sonra arka plan
--                            işiyle.
--   garage_tayfa_visibility  kişinin garajının bir tayfada görünüp
--                            görünmediği ("bu tayfada göster"). Satır yoksa
--                            varsayılan: tayfanın ilgi alanlarında
--                            `automotive` varsa görünür, yoksa görünmez.
--                            Tek kaynak `garage_visible_in(owner, tayfa)`.
--
-- Garaj bileşeni (`garage` modülü) yalnızca o tayfada görünen araçları,
-- yalnızca tayfanın etkin üyelerine ve yalnızca sahibi de etkin üyeyken
-- döner. Fotoğrafın görünürlüğü `garage_vehicle_seen_by(vehicle, viewer)`
-- ile aynı kuraldır: aracın görünür olduğu ve modülün açık olduğu ortak bir
-- tayfada ikisi de etkin üye.
--
-- `vehicles` bileşeni (00035) aracı garajdan seçebilir: event_vehicles.
-- vehicle_id garajdaki araca bağlanır (garaj aracı kalıcı silinince NULL
-- olur); marka/model/yıl/renk kopyalanır, plaka yalnızca etkinlik kaydında
-- kalır (bugünkü 30 gün kuralı). Bugüne kadar hep NULL olması gereken
-- kolondaki değerler (Faz 1 konvoy kaydından kopyalanmış, karşılığı olmayan
-- kimlikler) foreign key eklenmeden önce NULL yapılır.
--
-- Modül: `garage` tayfa_modules'a eklenir (CHECK genişler; ana sayfa
-- bileşeni `garage` bu modüle bağlıdır). Var olan standart tayfalarda açık
-- satır eklenir; anlık tayfada yoktur (kapalı).
--
-- Yükleme amacı `garage_vehicle` (tayfasız, avatar gibi), bağlama türü
-- `garage_vehicle`. Şikâyet hedefi `garage_vehicle` (konusu sahibidir).
--
-- Hesap silinince araçlar ve görünürlük satırları silinir (KVKK; fotoğraf
-- dosyaları silme kuyruğuna girer); başkasının aracını kaldıran moderatör
-- olarak kişi boşalır.
--
-- VERİ DEĞİŞTİREN migration (yalnızca event_vehicles.vehicle_id; yukarıda).
-- Başka veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down foreign key'i, tabloları ve fonksiyonları kaldırır,
-- `garage` modül satırlarını, `garage_vehicle` hedefli şikâyetleri ve
-- `garage_vehicle` amaçlı yüklemeleri siler (kısıtlara sığmadıkları için),
-- CHECK'leri eski haline getirir. Garajlar ve görünürlük ayarları kaybolur;
-- etkinlik kayıtlarındaki vehicle_id değerleri kalır (bağsız kimlik);
-- fotoğraf dosyaları nesne deposunda sahipsiz kalır. Uygulama kodu da
-- birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE garage_vehicles (
  id                uuid PRIMARY KEY,
  -- Sahibi; hesap silinince satır silinir.
  owner_id          uuid NOT NULL,
  -- Tek satır, kırpılmış, boşluklar tek.
  make              text NOT NULL,
  model             text,
  model_year        integer,
  color             text,
  note              text,
  -- Sahibinin fotoğrafları, sırasıyla.
  photo1_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  photo2_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  photo3_upload_id  uuid REFERENCES uploads (id) ON DELETE SET NULL,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL,
  deleted_at        timestamptz,
  -- Silen sahibi, moderatör ya da platform yöneticisi.
  deleted_by        uuid,

  CONSTRAINT garage_vehicles_make CHECK (
    char_length(make) BETWEEN 1 AND 40 AND make = btrim(make) AND make !~ '[[:cntrl:]]' AND make !~ ' {2}'),
  CONSTRAINT garage_vehicles_model CHECK (model IS NULL OR (
    char_length(model) BETWEEN 1 AND 40 AND model = btrim(model) AND model !~ '[[:cntrl:]]' AND model !~ ' {2}')),
  -- Üst sınır uygulamada güncel yıl + 1'dir; CHECK sabit kalmalıdır.
  CONSTRAINT garage_vehicles_model_year CHECK (model_year IS NULL OR model_year BETWEEN 1900 AND 2100),
  CONSTRAINT garage_vehicles_color CHECK (color IS NULL OR (
    char_length(color) BETWEEN 1 AND 30 AND color = btrim(color) AND color !~ '[[:cntrl:]]' AND color !~ ' {2}')),
  CONSTRAINT garage_vehicles_note CHECK (note IS NULL OR (
    char_length(note) BETWEEN 1 AND 200 AND note = btrim(note) AND note !~ '[[:cntrl:]]' AND note !~ ' {2}')),
  CONSTRAINT garage_vehicles_photos CHECK (
    (photo1_upload_id IS NULL OR photo2_upload_id IS NULL OR photo1_upload_id <> photo2_upload_id)
    AND (photo1_upload_id IS NULL OR photo3_upload_id IS NULL OR photo1_upload_id <> photo3_upload_id)
    AND (photo2_upload_id IS NULL OR photo3_upload_id IS NULL OR photo2_upload_id <> photo3_upload_id)),
  CONSTRAINT garage_vehicles_deleted CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT garage_vehicles_client_request_key UNIQUE (owner_id, client_request_id)
);
CREATE INDEX garage_vehicles_owner_idx ON garage_vehicles (owner_id, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX garage_vehicles_deleted_by_idx ON garage_vehicles (deleted_by) WHERE deleted_by IS NOT NULL;
CREATE INDEX garage_vehicles_deleted_at_idx ON garage_vehicles (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE UNIQUE INDEX garage_vehicles_photo1_upload_id_key ON garage_vehicles (photo1_upload_id) WHERE photo1_upload_id IS NOT NULL;
CREATE UNIQUE INDEX garage_vehicles_photo2_upload_id_key ON garage_vehicles (photo2_upload_id) WHERE photo2_upload_id IS NOT NULL;
CREATE UNIQUE INDEX garage_vehicles_photo3_upload_id_key ON garage_vehicles (photo3_upload_id) WHERE photo3_upload_id IS NOT NULL;

-- Fotoğraf araca bağlı, hazır ve doğru amaçlı olmalıdır (00017).
CREATE TRIGGER garage_vehicles_photo1_check BEFORE INSERT OR UPDATE OF photo1_upload_id ON garage_vehicles
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo1_upload_id', 'garage_vehicle', 'garage_vehicle');
CREATE TRIGGER garage_vehicles_photo2_check BEFORE INSERT OR UPDATE OF photo2_upload_id ON garage_vehicles
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo2_upload_id', 'garage_vehicle', 'garage_vehicle');
CREATE TRIGGER garage_vehicles_photo3_check BEFORE INSERT OR UPDATE OF photo3_upload_id ON garage_vehicles
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('photo3_upload_id', 'garage_vehicle', 'garage_vehicle');

-- Araç bir fotoğrafı bıraktığında ya da kalıcı silindiğinde dosya silme
-- kuyruğuna girer (materials_release_photos ile aynı; düzenleme
-- fotoğrafların sırasını değiştirebilir).
-- +goose StatementBegin
CREATE FUNCTION garage_vehicles_release_photos() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER garage_vehicles_photos_release
  AFTER UPDATE OF photo1_upload_id, photo2_upload_id, photo3_upload_id OR DELETE ON garage_vehicles
  FOR EACH ROW EXECUTE FUNCTION garage_vehicles_release_photos();

CREATE TABLE garage_tayfa_visibility (
  user_id    uuid NOT NULL,
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  visible    boolean NOT NULL,
  updated_at timestamptz NOT NULL,

  PRIMARY KEY (user_id, tayfa_id)
);
CREATE INDEX garage_tayfa_visibility_tayfa_idx ON garage_tayfa_visibility (tayfa_id);

-- Kişinin garajı tayfada görünür mü: satır varsa satırdaki seçim, yoksa
-- tayfanın ilgi alanlarında `automotive` olup olmadığı (ADR-0040 §4).
-- +goose StatementBegin
CREATE FUNCTION garage_visible_in(owner uuid, tayfa uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT COALESCE(
    (SELECT v.visible FROM garage_tayfa_visibility v WHERE v.user_id = $1 AND v.tayfa_id = $2),
    EXISTS (SELECT 1 FROM tayfa_interests i WHERE i.tayfa_id = $2 AND i.interest_key = 'automotive'))
$$;
-- +goose StatementEnd

-- Garaj aracını (ve fotoğraflarını) görebilen: aracın görünür olduğu,
-- `garage` modülü açık, silinmemiş ortak bir tayfada sahibiyle birlikte
-- etkin üye olan kişi. Sahibi her zaman görür (uygulama ayrıca denetler).
-- +goose StatementBegin
CREATE FUNCTION garage_vehicle_seen_by(vehicle uuid, viewer uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1
    FROM garage_vehicles g
    JOIN memberships o ON o.user_id = g.owner_id AND o.status = 'active'
    JOIN memberships w ON w.tayfa_id = o.tayfa_id AND w.user_id = $2 AND w.status = 'active'
    JOIN tayfas t ON t.id = o.tayfa_id AND t.deleted_at IS NULL
    JOIN tayfa_modules gm ON gm.tayfa_id = t.id AND gm.module = 'garage' AND gm.enabled
    WHERE g.id = $1 AND g.deleted_at IS NULL AND garage_visible_in(g.owner_id, t.id))
$$;
-- +goose StatementEnd

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): araç
-- başka kişiye geçmez, istek kimliği değişmez; bilgileri ve fotoğrafları
-- düzenlenir; silme yazılır; arka plan işi ve hesap silme kalıcı siler.
-- Görünürlük satırı başka kişiye ya da tayfaya taşınmaz.
GRANT SELECT, INSERT, DELETE ON garage_vehicles TO core_app;
GRANT UPDATE (make, model, model_year, color, note, photo1_upload_id, photo2_upload_id, photo3_upload_id,
              updated_at, deleted_at, deleted_by)
  ON garage_vehicles TO core_app;
GRANT SELECT, INSERT, DELETE ON garage_tayfa_visibility TO core_app;
GRANT UPDATE (visible, updated_at) ON garage_tayfa_visibility TO core_app;

-- Araçlar bileşeni: garajdan seçilen araç.
UPDATE event_vehicles SET vehicle_id = NULL WHERE vehicle_id IS NOT NULL;
ALTER TABLE event_vehicles ADD CONSTRAINT event_vehicles_vehicle_id_fkey
  FOREIGN KEY (vehicle_id) REFERENCES garage_vehicles (id) ON DELETE SET NULL;
CREATE INDEX event_vehicles_vehicle_id_idx ON event_vehicles (vehicle_id) WHERE vehicle_id IS NOT NULL;
GRANT UPDATE (vehicle_id) ON event_vehicles TO core_app;

-- Modül.
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library', 'question_pool', 'materials', 'garage'));
INSERT INTO tayfa_modules (tayfa_id, module, enabled)
SELECT t.id, 'garage', true FROM tayfas t WHERE t.kind = 'standard'
ON CONFLICT (tayfa_id, module) DO NOTHING;

-- Yükleme amacı (00063'teki tanımlar, yalnızca garage_vehicle eklenir).
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item', 'question_pool', 'material_note', 'garage_vehicle'));
-- Garaj kişiye aittir: yüklemesinin tayfası yoktur (00026).
ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK (
  (purpose IN ('avatar', 'data_export', 'dm_attachment', 'report_evidence', 'garage_vehicle')) = (tayfa_id IS NULL));
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
    OR (purpose = 'material_note' AND attached_type = 'material')
    OR (purpose = 'garage_vehicle' AND attached_type = 'garage_vehicle'))
);

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item', 'library_item', 'material', 'garage_vehicle'));

-- +goose Down

DELETE FROM reports WHERE target_type = 'garage_vehicle';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item', 'library_item', 'material'));

REVOKE UPDATE (vehicle_id) ON event_vehicles FROM core_app;
DROP INDEX event_vehicles_vehicle_id_idx;
ALTER TABLE event_vehicles DROP CONSTRAINT event_vehicles_vehicle_id_fkey;

DROP FUNCTION garage_vehicle_seen_by(uuid, uuid);
DROP FUNCTION garage_visible_in(uuid, uuid);
DROP TABLE garage_tayfa_visibility;
DROP TABLE garage_vehicles;
DROP FUNCTION garage_vehicles_release_photos();

DELETE FROM uploads WHERE purpose = 'garage_vehicle';
ALTER TABLE uploads DROP CONSTRAINT uploads_tayfa;
ALTER TABLE uploads ADD CONSTRAINT uploads_tayfa CHECK (
  (purpose IN ('avatar', 'data_export', 'dm_attachment', 'report_evidence')) = (tayfa_id IS NULL));
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
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN
  ('avatar', 'log_photo', 'cover', 'data_export', 'event_photo', 'chat_photo', 'dm_attachment', 'report_evidence',
   'library_item', 'question_pool', 'material_note'));

DELETE FROM tayfa_modules WHERE module = 'garage';
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN
  ('announcements', 'events', 'qa', 'polls', 'daily', 'library', 'question_pool', 'materials'));
