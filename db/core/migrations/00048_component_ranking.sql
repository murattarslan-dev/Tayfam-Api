-- Derece / sıralama bileşeni (`ranking`; issue #253; ADR-0025 §4, §5;
-- ADR-0037 §3).
--
--   event_ranking_entries  elle girilen derecelerde katılımcı başına bir
--                          değer: `metric: duration` ise süre (saniye),
--                          `metric: rank` ise sıra (1, 2, 3 …). Değeri
--                          organizatör, moderatör, yönetici ve organizatörün
--                          atadığı hakemler girer; satır son yazanı
--                          (entered_by) tutar. Sıra (rank) saklanmaz,
--                          okunurken değerlerden ve yönden (`direction`)
--                          hesaplanır.
--
-- Ölçümden hesaplanan derecede (`source: <ölçüm anahtarı>`) bu tabloya hiç
-- yazılmaz: sıralama okunurken aynı etkinlikteki kişisel ölçümün kaydedilmiş
-- değerlerinden (event_measurements) hesaplanır, çift giriş yoktur.
--
-- Tablo (event_id, component_key) ile etkinliğe ve tür sürümündeki bileşene
-- bağlıdır; ortak tetikleyici (event_component_guard('ranking'), 00033)
-- bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Kişisel veri (ADR-0025 §11): değer kişiye aittir, dışa aktarmaya girer ve
-- hesap silmede silinir; başkasının değerini yazan kişi silinince yazan
-- boşalır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde derece
-- bileşeni yok).
--
-- Geri alma planı: Down tabloyu kaldırır; elle girilmiş dereceler kaybolur,
-- ölçümden hesaplanan derecelerin kaynağı (ölçümler) kalır. Uygulama kodu da
-- birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_ranking_entries (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'ranking').
  component_key text NOT NULL,
  -- Derecenin ait olduğu katılımcı. Hesap silinince satır silinir.
  user_id       uuid NOT NULL,
  -- Süre (saniye, en çok üç basamak) ya da sıra (tam sayı); ölçüte göre
  -- sınırlar uygulama katmanındadır, burada yalnızca genel sınır vardır.
  value         numeric(13, 3) NOT NULL,
  -- Değeri son yazan kişi. Hesap silinince boşaltılır.
  entered_by    uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_ranking_entries_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_ranking_entries_value CHECK (value > 0 AND value <= 1000000),
  -- Kişi başına bir değer.
  CONSTRAINT event_ranking_entries_person UNIQUE (event_id, component_key, user_id)
);
-- Dışa aktarma ve hesap silme.
CREATE INDEX event_ranking_entries_user_id_idx ON event_ranking_entries (user_id);
CREATE INDEX event_ranking_entries_entered_by_idx ON event_ranking_entries (entered_by) WHERE entered_by IS NOT NULL;
CREATE TRIGGER event_ranking_entries_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_ranking_entries
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('ranking');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): değer
-- değişir (ON CONFLICT) ve kaldırılır; hesap silmede yazan boşaltılır. Satır
-- başka etkinliğe, bileşene ya da kişiye taşınamaz. SELECT ve INSERT burada da
-- açıkça verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_ranking_entries TO core_app;
GRANT UPDATE (value, entered_by, updated_at) ON event_ranking_entries TO core_app;

-- +goose Down

DROP TABLE event_ranking_entries;
