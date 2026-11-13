-- Kanal kitlesi: etkinliğe gelenlerin ve seçili üyelerin kanalları (issue
-- #183, ADR-0024; platform sahibinin kararı 2026-11-13, Notion Proje Planı
-- §6 Sohbet).
--
-- channels.visibility iki değer daha alır:
--   event     etkinliğe gelenler: kanalı açan, organizatör ve "geliyorum"
--             diyenler. Kitle core'un katılım verisidir ve her istekte
--             çağıranın token'ıyla core'a sorulur; chat katılımın kopyasını
--             tutmaz. event_id core'un etkinlik kimliğidir (veritabanları
--             arası foreign key yok, ADR-0002).
--   selected  seçili üyeler: kanalı açan ve seçtikleri (channel_members).
-- Yönetim (yönetici ve moderatörler) bu iki türe rolüyle girmez.
--
-- archive_at: etkinlik kanalının planlanan arşiv zamanı, etkinliğin bitişi
--   ya da iptal anı + 7 gün. archive_source_at bu bilginin core'daki
--   zamanıdır; olaylar sırasız gelebildiği için yalnızca daha yeni bilgi
--   uygulanır (ADR-0002). Zamanı gelen kanal arka plan işiyle arşivlenir
--   (archived_at = archive_at); o ana kadar da yazmaya kapalıdır.
-- Bir etkinliğin aynı anda tek açık (arşivlenmemiş) kanalı olur.
--
-- channel_members: seçili üye kanalının üyeleri (kanalı açan dahil). Kişi
--   tayfadan ayrılınca, çıkarılınca ya da hesabı silinince satırları
--   silinir; kanal silinince CASCADE. Yalnızca kimlikler tutulur.
--
-- Geri alma planı: Down yeni türdeki kanalları (mesajlarıyla) ve
-- channel_members'ı siler, kolonları ve kısıtları eski haline döndürür.

-- +goose Up
ALTER TABLE channels
  ADD COLUMN event_id uuid,
  ADD COLUMN archive_at timestamptz,
  ADD COLUMN archive_source_at timestamptz;

ALTER TABLE channels DROP CONSTRAINT channels_visibility;
ALTER TABLE channels ADD CONSTRAINT channels_visibility CHECK (visibility IN ('members', 'staff', 'event', 'selected'));
ALTER TABLE channels ADD CONSTRAINT channels_event CHECK ((visibility = 'event') = (event_id IS NOT NULL));
ALTER TABLE channels ADD CONSTRAINT channels_archive_at CHECK (
  (archive_at IS NULL) = (archive_source_at IS NULL) AND (archive_at IS NULL OR visibility = 'event'));

CREATE UNIQUE INDEX channels_event_open ON channels (event_id) WHERE event_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX channels_archive_due_idx ON channels (archive_at) WHERE archive_at IS NOT NULL AND archived_at IS NULL;

CREATE TABLE channel_members (
  channel_id uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  added_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX channel_members_user_id_idx ON channel_members (user_id);

GRANT SELECT, INSERT, DELETE ON channel_members TO chat_app;

-- +goose Down
DROP TABLE channel_members;
DELETE FROM channels WHERE visibility IN ('event', 'selected');
DROP INDEX channels_archive_due_idx;
DROP INDEX channels_event_open;
ALTER TABLE channels DROP CONSTRAINT channels_archive_at;
ALTER TABLE channels DROP CONSTRAINT channels_event;
ALTER TABLE channels DROP CONSTRAINT channels_visibility;
ALTER TABLE channels ADD CONSTRAINT channels_visibility CHECK (visibility IN ('members', 'staff'));
ALTER TABLE channels
  DROP COLUMN archive_source_at,
  DROP COLUMN archive_at,
  DROP COLUMN event_id;
