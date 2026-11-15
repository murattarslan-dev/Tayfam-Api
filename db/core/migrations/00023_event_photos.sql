-- Etkinlik fotoğraf albümü (issue #206; ADR-0028; Notion "Faz 2 (v2) —
-- Medya ve Sosyal" §2; veri modeli v15 "event_photos").
--
-- Bir etkinliğin silinmemiş event_photos satırları onun albümüdür. Dosya
-- uploads'tadır (purpose event_photo, attached_type event_photo); yükleme
-- akışı ADR-0020'nin aynısıdır. Fotoğrafı yalnızca o etkinlikte katılımı
-- doğrulanmış (attended) kişi ekler; kişi başı 30, albüm başına 500
-- silinmemiş fotoğraf. Uygulama katmanı aynı kuralları önce denetler; bu
-- dosyadaki trigger ikinci savunma hattıdır ve etkinlik satırını
-- kilitleyerek eşzamanlı eklemede de sınırı korur. Albüm fotoğrafı kapak
-- olamaz: kapak kolonlarının trigger'ı yalnızca purpose = cover kabul eder.
--
-- Geri alma planı: Down albüm satırlarını ve event_photo dosyalarını
-- doğrudan siler (R2'deki nesneler sahipsiz kalır; media/ önekindeki
-- sahipsiz nesneler elle ya da lifecycle kuralıyla temizlenir), albüm
-- şikâyetlerini siler ve kısıtları 00022'deki haline döndürür.

-- +goose Up
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover', 'data_export', 'event_photo'));
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

CREATE TABLE event_photos (
  id                uuid PRIMARY KEY,
  event_id          uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  uploader_id       uuid NOT NULL,
  -- Dosya giderse (silme kuyruğundan sonra satırı silinir) fotoğraf da gider.
  upload_id         uuid NOT NULL REFERENCES uploads (id) ON DELETE CASCADE,
  caption           text,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  deleted_by        uuid,

  CONSTRAINT event_photos_upload_key UNIQUE (upload_id),
  CONSTRAINT event_photos_client_request_key UNIQUE (uploader_id, client_request_id),
  CONSTRAINT event_photos_caption CHECK (caption IS NULL OR char_length(caption) BETWEEN 1 AND 200),
  -- deleted_by hesap silmede NULL yapılabilir; deleted_at kalır.
  CONSTRAINT event_photos_deleted CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
-- Albüm sayfası ve sınırlar.
CREATE INDEX event_photos_event_idx ON event_photos (event_id, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX event_photos_uploader_idx ON event_photos (uploader_id, event_id) WHERE deleted_at IS NULL;
-- Saklama işi: 30 günü geçen soft delete'ler.
CREATE INDEX event_photos_deleted_idx ON event_photos (deleted_at) WHERE deleted_at IS NOT NULL;

-- Ekleme kuralı (ikinci hat): etkinlik satırı kilitlenir; iptal edilmiş ya
-- da silinmiş etkinliğe, katılımı doğrulanmamış kişiden ve sınır dolmuşken
-- fotoğraf eklenemez. Pencere (saat) yalnızca uygulamada denetlenir
-- (ADR-0028 §2). Mesaj kişiyi ve dosyayı adlandırmaz.
-- +goose StatementBegin
CREATE FUNCTION event_photos_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  ev_status text;
BEGIN
  SELECT status INTO ev_status FROM events
  WHERE id = NEW.event_id AND deleted_at IS NULL
  FOR UPDATE;
  IF NOT FOUND OR ev_status = 'cancelled' THEN
    RAISE EXCEPTION 'albüm kapalı' USING ERRCODE = 'TF090';
  END IF;
  IF NEW.deleted_at IS NOT NULL THEN
    RAISE EXCEPTION 'silinmiş fotoğraf eklenemez' USING ERRCODE = 'TF090';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM event_rsvps
    WHERE event_id = NEW.event_id AND user_id = NEW.uploader_id AND attendance = 'attended'
  ) THEN
    RAISE EXCEPTION 'katılım doğrulanmamış' USING ERRCODE = 'TF090';
  END IF;
  IF (SELECT count(*) FROM event_photos
      WHERE event_id = NEW.event_id AND uploader_id = NEW.uploader_id AND deleted_at IS NULL) >= 30 THEN
    RAISE EXCEPTION 'kişi başı fotoğraf sınırı' USING ERRCODE = 'TF090';
  END IF;
  IF (SELECT count(*) FROM event_photos
      WHERE event_id = NEW.event_id AND deleted_at IS NULL) >= 500 THEN
    RAISE EXCEPTION 'albüm dolu' USING ERRCODE = 'TF090';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_photos_guard BEFORE INSERT ON event_photos
  FOR EACH ROW EXECUTE FUNCTION event_photos_guard();

-- Dosya yalnızca bu satıra bağlı, hazır event_photo olabilir; satır kalıcı
-- silinince (cascade dahil) dosya silme kuyruğuna girer (00017).
CREATE TRIGGER event_photos_upload_check BEFORE INSERT OR UPDATE OF upload_id ON event_photos
  FOR EACH ROW EXECUTE FUNCTION uploads_check_ref('upload_id', 'event_photo', 'event_photo');
CREATE TRIGGER event_photos_upload_release AFTER UPDATE OF upload_id OR DELETE ON event_photos
  FOR EACH ROW EXECUTE FUNCTION uploads_release_ref('upload_id');

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message', 'event_photo'));

-- core_app (ADR-0015): ekler, okur, soft delete yapar, kalıcı siler (saklama
-- işi, hesap silme). Fotoğraf başka etkinliğe ya da dosyaya taşınamaz,
-- yükleyeni ve açıklaması değiştirilemez.
GRANT SELECT, INSERT, DELETE ON event_photos TO core_app;
GRANT UPDATE (deleted_at, deleted_by) ON event_photos TO core_app;

-- +goose Down
DELETE FROM reports WHERE target_type = 'event_photo';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'fishing_catch', 'dm_message'));
DROP TABLE event_photos;
DROP FUNCTION event_photos_guard();
DELETE FROM uploads WHERE purpose = 'event_photo';
ALTER TABLE uploads DROP CONSTRAINT uploads_attached;
ALTER TABLE uploads ADD CONSTRAINT uploads_attached CHECK (
  (attached_type IS NULL) = (attached_id IS NULL)
  AND (attached_type IS NULL) = (attached_at IS NULL)
  AND (attached_type IS NULL OR status IN ('ready', 'deleting'))
  AND (attached_type IS NULL
    OR (purpose = 'avatar' AND attached_type = 'user')
    OR (purpose = 'catch_photo' AND attached_type = 'fishing_catch')
    OR (purpose = 'cover' AND attached_type IN ('tayfa', 'event')))
);
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'catch_photo', 'cover', 'data_export'));
