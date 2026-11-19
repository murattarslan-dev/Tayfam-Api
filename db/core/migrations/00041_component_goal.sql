-- Hedef çubuğu bileşeni (`goal`; issue #246; ADR-0025 §4, §5).
--
--   event_goals  bir etkinliğin bir hedef bileşeninin hedef değeri. Bileşen
--                tür tanımında bir ölçüme bağlıdır (`measure` parametresi,
--                katalog doğrulaması); hedef o ölçümün biriminde bir
--                sayıdır. Organizatör etkinliğe hedef koymadıysa satır
--                yoktur ve tanımdaki `default_target` geçerlidir. İlerleme
--                tabloya yazılmaz: okunurken bağlı ölçümün kaydedilmiş
--                değerlerinden (event_measurements; toplam ölçümde tek
--                değer, kişisel ölçümde değerlerin toplamı) hesaplanır,
--                önerilen değer (`prefill_from`) sayılmaz.
--
-- Satır kişi kimliği taşımaz (hedefi kimin koyduğu yalnızca denetim
-- kaydındadır): hedef etkinliğin planıdır, kişisel veri değildir; dışa
-- aktarmaya ve hesap silmeye girmez.
--
-- Tablo (event_id, component_key) ile etkinliğe ve tür sürümündeki bileşene
-- bağlıdır; ortak tetikleyici (event_component_guard('goal'), 00033)
-- bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde hedef
-- bileşeni yok).
--
-- Geri alma planı: Down tabloyu kaldırır; etkinliklere konmuş hedefler
-- kaybolur, tanımdaki varsayılan hedefler kalır. Uygulama kodu da birlikte
-- geri alınmalıdır.

-- +goose Up

CREATE TABLE event_goals (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'goal').
  component_key text NOT NULL,
  -- Hedef; bağlı ölçümün biriminde, sıfırdan büyük.
  target        numeric(13, 3) NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Hedefin son yazıldığı an; uygulama yazar (isteğin zamanı).
  updated_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_goals_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_goals_target CHECK (target > 0 AND target <= 1000000000)
);
CREATE TRIGGER event_goals_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_goals
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('goal');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): hedef
-- değişir (ON CONFLICT) ve kaldırılır; satır başka etkinliğe ya da bileşene
-- taşınamaz. SELECT ve INSERT burada da açıkça verilir: geri alınıp yeniden
-- uygulanan migration'ı başka bir rol çalıştırırsa varsayılan yetkiler
-- oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_goals TO core_app;
GRANT UPDATE (target, updated_at) ON event_goals TO core_app;

-- +goose Down

DROP TABLE event_goals;
