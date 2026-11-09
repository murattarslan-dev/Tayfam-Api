-- Duyurular (veri modeli v14 "Duyuru ve Etkinlik", "İndeksler", "Silinen
-- içeriği geri alma", "Çevrimdışı yazma"; issue #48).
--
-- Duyuruyu yönetici ve moderatör yayınlar (karar 2026-11-09); izinler
-- core'daki izin tablosundadır (internal/core/app/authz).
--
-- author_id identity'nin kullanıcısıdır; veritabanları arası foreign key
-- yoktur (ADR-0009). Hesap silinince NULL olur ve istemci "Silinmiş
-- kullanıcı" gösterir.
--
-- Silme soft delete'tir (deleted_at, deleted_by):
--   Yazarın kendi silmesi: başlık ve gövde hemen boşaltılır, geri alınamaz.
--   Moderatör/yönetici silmesi: içerik gizlenir, 30 gün içinde yönetici geri
--     alabilir. Kalıcı silme işi ayrıdır.

-- +goose Up
CREATE TABLE announcements (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  author_id         uuid,
  title             text NOT NULL,
  body              text NOT NULL DEFAULT '',
  pinned            boolean NOT NULL DEFAULT false,
  -- Aynı yayınlama isteğinin tekrar gönderilmesine karşı (v14 "Çevrimdışı
  -- yazma"): UNIQUE(yazar, client_request_id).
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  deleted_by        uuid,

  -- Görünen duyurunun başlığı vardır; yazarın kendi sildiği duyurunun
  -- başlığı ve gövdesi boşaltılmıştır.
  CONSTRAINT announcements_title_len CHECK (
    char_length(title) <= 120 AND (deleted_at IS NOT NULL OR char_length(title) >= 1)
  ),
  CONSTRAINT announcements_body_len CHECK (char_length(body) <= 5000),
  CONSTRAINT announcements_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
CREATE UNIQUE INDEX announcements_client_request_key ON announcements (author_id, client_request_id);
-- Duyuru akışı: sabitlenenler önce, yeniden eskiye (id sayfalama için).
CREATE INDEX announcements_tayfa_feed_idx ON announcements (tayfa_id, pinned DESC, created_at DESC, id DESC) WHERE deleted_at IS NULL;
-- Hesap silmede author_id'yi boşaltmak için.
CREATE INDEX announcements_author_id_idx ON announcements (author_id) WHERE author_id IS NOT NULL;
-- 30 günlük kalıcı silme işi.
CREATE INDEX announcements_deleted_at_idx ON announcements (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER announcements_updated_at BEFORE UPDATE ON announcements FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE announcements;
