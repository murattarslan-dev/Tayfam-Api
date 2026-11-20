-- Kutlama bileşeni (`celebration`; issue #255; ADR-0025 §4, §5; ADR-0037
-- §6; ADR-0039 §1–§2).
--
--   event_celebrations         organizatörün kazananı ilan ettiği an: bileşen
--                              başına bir satır. İlk ilan tayfaya uygulama
--                              içi `celebration` bildirimi gönderir (push
--                              yok); yeniden ilan bildirim göndermez.
--   event_celebration_winners  kaynak bileşeni olmayan (yalın) kutlamada
--                              organizatörün katılımcılar arasından seçtiği
--                              kazananlar. Kaynaklı kutlamada (`source`:
--                              yazboz, eşleşme ağacı, derece, oylama) bu
--                              tabloya yazılmaz: kazanan okunurken kaynağın
--                              kaydedilmiş sonucundan hesaplanır, çift kayıt
--                              yoktur.
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('celebration'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Kişisel veri (ADR-0025 §11): elle seçilen kazanan kişiye aittir, dışa
-- aktarmaya girer ve hesap silmede silinir; ilan eden silinince ilan eden
-- boşalır. Misafir oyuncu hiçbir tabloya kazanan olarak yazılmaz (ADR-0037 §6).
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde kutlama
-- bileşeni yok).
--
-- Geri alma planı: Down tabloları kaldırır; ilanlar ve elle seçilen
-- kazananlar kaybolur, kaynakların sonuçları kalır. Gönderilmiş bildirimler
-- chat'te kalır. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_celebrations (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'celebration').
  component_key text NOT NULL,
  -- İlan eden yönetici. Hesap silinince boşaltılır.
  announced_by  uuid,
  announced_at  timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_celebrations_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_celebrations_times CHECK (updated_at >= announced_at)
);
-- Hesap silme.
CREATE INDEX event_celebrations_announced_by_idx ON event_celebrations (announced_by) WHERE announced_by IS NOT NULL;
CREATE TRIGGER event_celebrations_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_celebrations
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('celebration');

CREATE TABLE event_celebration_winners (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  -- Kazanan katılımcı. Hesap silinince satır silinir.
  user_id       uuid NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  -- Kazanan yalnızca ilan edilmiş bir kutlamada vardır.
  CONSTRAINT event_celebration_winners_celebration FOREIGN KEY (event_id, component_key)
    REFERENCES event_celebrations (event_id, component_key) ON DELETE CASCADE,
  CONSTRAINT event_celebration_winners_person UNIQUE (event_id, component_key, user_id)
);
-- Dışa aktarma ve hesap silme.
CREATE INDEX event_celebration_winners_user_id_idx ON event_celebration_winners (user_id);
CREATE TRIGGER event_celebration_winners_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_celebration_winners
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('celebration');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): ilan
-- satırı silinmez ve başka etkinliğe ya da bileşene taşınamaz; yeniden
-- ilanda yalnızca updated_at, hesap silmede ilan eden değişir. Elle seçilen
-- kazananlar yeniden ilanda silinip yeniden yazılır. SELECT ve INSERT burada
-- da açıkça verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT ON event_celebrations TO core_app;
GRANT UPDATE (announced_by, updated_at) ON event_celebrations TO core_app;
GRANT SELECT, INSERT, DELETE ON event_celebration_winners TO core_app;

-- +goose Down

DROP TABLE event_celebration_winners;
DROP TABLE event_celebrations;
