-- Şimdi başlat: açık uçlu etkinlik ve tayfanın şimdi başlatma ayarı
-- (issue #263; ADR-0038 §1, §2; Notion "Faz 3 — Katalog ayrıntıları ve
-- şema ekleri (2026-11-18)" §D).
--
--   events.open_ended          sunucu saatiyle "şimdi" başlatılmış, bitişi
--       planlanmamış etkinlik. starts_at = oluşturma anı, ends_at = en geç
--       12 saat sonrası (kendiliğinden biter; iş gerekmez). Organizatör
--       "Bitir" deyince ends_at = o an olur ve bayrak kalkar. Seriye ait
--       olamaz, Keşfet'e çıkamaz; ends_at 12 saati aşamaz
--       (events_open_ended CHECK'i, ikinci savunma hattı; ADR-0011).
--   tayfas.now_create_min_role tayfada Şimdi başlat için gereken en düşük
--       rol (member varsayılan / moderator / admin); izin tablosundaki
--       EventCreateNow işlemini ayarlar. Yalnızca yönetici değiştirir.
--   events_open_now_idx        kişi başına en fazla 1 açık şimdi etkinliği
--       sınırının sayımı için.
--
-- QR katılımı (#264) ve şimdi bildirimi (#265) kendi migration'larıyla
-- gelir. Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down indeksi, CHECK'leri ve iki kolonu kaldırır. Açık
-- uçlu etkinlikler sıradan (ends_at'i yazılı) etkinlik olarak kalır; tayfa
-- ayarı kaybolur (Şimdi başlat yeniden EventCreate'e bağlanmalıdır).
-- Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

ALTER TABLE events
  ADD COLUMN open_ended boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT events_open_ended CHECK (
    NOT open_ended
    OR (series_id IS NULL AND NOT discoverable AND ends_at <= starts_at + interval '12 hours')
  );

CREATE INDEX events_open_now_idx ON events (created_by)
  WHERE open_ended AND deleted_at IS NULL AND status = 'scheduled';

ALTER TABLE tayfas
  ADD COLUMN now_create_min_role text NOT NULL DEFAULT 'member',
  ADD CONSTRAINT tayfas_now_create_min_role CHECK (now_create_min_role IN ('member', 'moderator', 'admin'));

-- +goose Down

ALTER TABLE tayfas
  DROP CONSTRAINT tayfas_now_create_min_role,
  DROP COLUMN now_create_min_role;

DROP INDEX events_open_now_idx;

ALTER TABLE events
  DROP CONSTRAINT events_open_ended,
  DROP COLUMN open_ended;
