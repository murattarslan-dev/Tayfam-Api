-- Program akışı bileşeni (`schedule`; issue #247; ADR-0025 §4, §5).
--
--   event_schedule_items  bir etkinliğin bir program bileşenindeki saatli
--                         maddeler: başlangıç saati, isteğe bağlı bitiş
--                         saati, başlık (1–120) ve isteğe bağlı not
--                         (1–500). Maddeler başlangıç saatine göre
--                         sıralanır; aynı saattekiler eklenme sırasıyla
--                         (created_at). Madde sayısı bileşenin
--                         `max_items` parametresiyle sınırlıdır (uygulama
--                         denetler; tanım vermezse 100).
--
-- Satır kişi kimliği taşımaz (maddeyi kimin yazdığı yalnızca denetim
-- kaydındadır): program etkinliğin planıdır, kişisel veri değildir; dışa
-- aktarmaya ve hesap silmeye girmez. Seri örneğine saatleri örneğin
-- başlangıcına kaydırılarak kopyalanır (CopyScheduleItems).
--
-- Tablo (event_id, component_key) ile etkinliğe ve tür sürümündeki bileşene
-- bağlıdır; ortak tetikleyici (event_component_guard('schedule'), 00033)
-- bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde program
-- bileşeni yok).
--
-- Geri alma planı: Down tabloyu kaldırır; etkinliklerin program maddeleri
-- kaybolur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_schedule_items (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'schedule').
  component_key text NOT NULL,
  starts_at     timestamptz NOT NULL,
  -- İsteğe bağlı; varsa başlangıçtan sonra.
  ends_at       timestamptz,
  title         text NOT NULL,
  note          text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Maddenin son yazıldığı an; uygulama yazar (isteğin zamanı).
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_schedule_items_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_schedule_items_title_len CHECK (char_length(title) BETWEEN 1 AND 120),
  CONSTRAINT event_schedule_items_note_len CHECK (note IS NULL OR char_length(note) BETWEEN 1 AND 500),
  CONSTRAINT event_schedule_items_ends CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX event_schedule_items_event_idx ON event_schedule_items (event_id, component_key, starts_at, created_at, id);
CREATE TRIGGER event_schedule_items_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_schedule_items
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('schedule');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): madde
-- düzenlenir ve silinir; başka etkinliğe ya da bileşene taşınamaz. SELECT ve
-- INSERT burada da açıkça verilir: geri alınıp yeniden uygulanan migration'ı
-- başka bir rol çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_schedule_items TO core_app;
GRANT UPDATE (starts_at, ends_at, title, note, updated_at) ON event_schedule_items TO core_app;

-- +goose Down

DROP TABLE event_schedule_items;
