-- Eşleşme ağacı bileşeni (`bracket`; issue #251; ADR-0025 §4, §5, §6,
-- ADR-0037 §3).
--
--   event_brackets         bir etkinliğin bir eşleşme ağacı bileşeninde
--                          başlatılan turnuva: biçim (eleme / lig /
--                          İsviçre), maç başına oyun (BO1/BO3/BO5), skor
--                          kuralı (puan ya da galibiyet-beraberlik-
--                          mağlubiyet; yüksek ya da düşük kazanır), planlanan
--                          tur sayısı ve tohumların geldiği kura bileşeni.
--                          Kurallar başlatırken çözülüp burada saklanır;
--                          sonradan değişen seçim başlamış turnuvayı
--                          değiştirmez.
--   event_bracket_matches  turların maçları. Taraflar kuradaki yerlerdir
--                          (event_draw_entries.position): kişi ya da takım
--                          kuradan okunur, burada tutulmaz. Boş taraf bay
--                          geçmedir (eleme ve İsviçre).
--   event_bracket_games    bir maçın oyunları (BO maçında birden çok; eleme
--                          maçında beraberlikte ek oyun). `points` kuralında
--                          iki tarafın puanı, `wdl` kuralında A tarafının
--                          sonucu.
--
-- Maçın kazananı, sıralama (puan → aralarındaki maç → Buchholz) ve
-- turnuvanın kazananı saklanmaz; oyunlardan hesaplanır (yazboz kuralı,
-- internal/core/app/component/scoreboard: Compute, Decided).
--
-- Kişisel veri: tablolarda kişi yoktur. Kişi kuradaki yeri üzerinden
-- bağlıdır; hesap silmede kura yeri boşalır (00044) ve maç "silinmiş
-- kullanıcı" olarak kalır. Kişinin maçları ve kendi skorları dışa
-- aktarmaya girer (ExportBracketMatches). Turnuva katılımdır; seri örneğine
-- kopyalanmaz.
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('bracket'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
-- Maçın tarafları aynı etkinliğin kurasındaki yerlere bileşik yabancı
-- anahtarla bağlıdır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları kaldırır; başlatılmış turnuvalar ve
-- skorları kaybolur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_brackets (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'bracket').
  component_key text NOT NULL,
  -- Tohumların geldiği kura bileşeninin anahtarı.
  draw_key      text NOT NULL,
  format        text NOT NULL,
  best_of       smallint NOT NULL,
  result        text NOT NULL,
  winner        text NOT NULL,
  -- Planlanan tur sayısı: lig için fikstürün turları, İsviçre için
  -- oynanacak tur, eleme için en çok tur.
  rounds        smallint NOT NULL,
  started_at    timestamptz NOT NULL,

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_brackets_draw UNIQUE (event_id, component_key, draw_key),
  CONSTRAINT event_brackets_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_brackets_draw_key CHECK (draw_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_brackets_format CHECK (format IN ('elimination', 'league', 'swiss')),
  CONSTRAINT event_brackets_best_of CHECK (best_of IN (1, 3, 5)),
  CONSTRAINT event_brackets_result CHECK (result IN ('points', 'wdl')),
  CONSTRAINT event_brackets_winner CHECK (winner IN ('high', 'low')),
  CONSTRAINT event_brackets_rounds CHECK (rounds BETWEEN 1 AND 64)
);
CREATE TRIGGER event_brackets_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_brackets
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('bracket');

CREATE TABLE event_bracket_matches (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  draw_key      text NOT NULL,
  round         smallint NOT NULL,
  -- Maçın turdaki sırası.
  slot          smallint NOT NULL,
  -- Kuradaki yerler; NULL taraf bay (rakipsiz) ya da boş yerdir.
  a_position    smallint,
  b_position    smallint,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_bracket_matches_round CHECK (round BETWEEN 1 AND 64),
  CONSTRAINT event_bracket_matches_slot CHECK (slot BETWEEN 1 AND 256),
  CONSTRAINT event_bracket_matches_sides CHECK (a_position IS DISTINCT FROM b_position OR a_position IS NULL),
  CONSTRAINT event_bracket_matches_slot_uniq UNIQUE (event_id, component_key, round, slot),
  CONSTRAINT event_bracket_matches_ref UNIQUE (id, event_id, component_key),
  CONSTRAINT event_bracket_matches_bracket FOREIGN KEY (event_id, component_key, draw_key)
    REFERENCES event_brackets (event_id, component_key, draw_key) ON DELETE CASCADE,
  CONSTRAINT event_bracket_matches_a FOREIGN KEY (event_id, draw_key, a_position)
    REFERENCES event_draw_entries (event_id, component_key, position),
  CONSTRAINT event_bracket_matches_b FOREIGN KEY (event_id, draw_key, b_position)
    REFERENCES event_draw_entries (event_id, component_key, position)
);
CREATE TRIGGER event_bracket_matches_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_bracket_matches
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('bracket');

CREATE TABLE event_bracket_games (
  match_id      uuid NOT NULL,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  number        smallint NOT NULL,
  -- `points` kuralında iki tarafın puanı; `wdl` kuralında A tarafının
  -- sonucu (B'ninki tersidir).
  a_points      integer,
  b_points      integer,
  a_outcome     text,
  created_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (match_id, number),
  CONSTRAINT event_bracket_games_number CHECK (number BETWEEN 1 AND 9),
  CONSTRAINT event_bracket_games_points CHECK (
    (a_points IS NULL OR a_points BETWEEN -1000000 AND 1000000)
    AND (b_points IS NULL OR b_points BETWEEN -1000000 AND 1000000)),
  CONSTRAINT event_bracket_games_outcome CHECK (a_outcome IS NULL OR a_outcome IN ('win', 'draw', 'loss')),
  CONSTRAINT event_bracket_games_one CHECK (
    (a_points IS NOT NULL AND b_points IS NOT NULL AND a_outcome IS NULL)
    OR (a_points IS NULL AND b_points IS NULL AND a_outcome IS NOT NULL)),
  CONSTRAINT event_bracket_games_match FOREIGN KEY (match_id, event_id, component_key)
    REFERENCES event_bracket_matches (id, event_id, component_key) ON DELETE CASCADE
);
CREATE INDEX event_bracket_games_event_idx ON event_bracket_games (event_id, component_key);
CREATE TRIGGER event_bracket_games_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_bracket_games
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('bracket');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016). Bir
-- turnuva, maç ya da oyun başka etkinliğe, bileşene ya da maça geçemez:
-- UPDATE yetkisi yoktur. Düzeltme oyunu silip yeniden yazar; organizatör
-- turnuvayı sıfırlayabilir (DELETE). SELECT ve INSERT burada da açıkça
-- verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_brackets TO core_app;
GRANT SELECT, INSERT, DELETE ON event_bracket_matches TO core_app;
GRANT SELECT, INSERT, DELETE ON event_bracket_games TO core_app;

-- +goose Down

DROP TABLE event_bracket_games;
DROP TABLE event_bracket_matches;
DROP TABLE event_brackets;
