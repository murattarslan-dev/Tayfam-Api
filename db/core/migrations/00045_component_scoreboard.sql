-- Yazboz bileşeni (`scoreboard`; issue #250; ADR-0025 §4–§6, ADR-0037 §5).
--
--   catalog_score_presets  oyun kuralları ön ayarları (Batak, 101 Okey …).
--                          Ön ayar katalog verisidir: yeni oyun uygulama
--                          sürümü istemez. Tür tanımındaki yazboz bileşeni
--                          hangi ön ayarların seçilebileceğini `presets`
--                          parametresiyle listeler. core_app yalnızca okur.
--   event_score_tables     bir etkinliğin bir yazboz bileşenindeki masalar
--                          (bileşen başına en fazla 20). Masa kurulurken
--                          kurallar (taraf, el el / tek maç / ilk N, hedef
--                          puan, kazanan, sonuç modu, BO) bileşen
--                          parametrelerinden ve ön ayardan çözülüp masaya
--                          yazılır: ön ayar sonradan değişse de oynanmış
--                          masanın anlamı değişmez.
--   event_score_players    masadaki yerler: kişi ya da (takım yazbozunda)
--                          bir takım bileşeninin takımı. Hesabı silinen
--                          kişinin yeri boşalır ve "Silinmiş kullanıcı"
--                          olarak kalır (ADR-0025 §11); silinen takımın yeri
--                          de boşalır. Misafir oyuncu (#252) bu tabloya
--                          kendi sütununu ekler.
--   event_scores           el (tur) başına bir yerin skoru: puan
--                          (`points`) ya da galibiyet/beraberlik/mağlubiyet
--                          (`wdl`).
--
-- Skoru organizatör ve masaya (ya da bileşene, etkinliğe) atanmış hakem
-- girer; düzeltmeyi organizatör yapar (ADR-0025 §5, event_referees). Kimin
-- girdiği denetim kaydındadır, tabloda tutulmaz. Para, bahis ya da ödül
-- alanı yoktur (ADR-0025 §4).
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('scoreboard'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Veri taşınmaz ve silinmez. Tür tanımları değişmez (catalog_version
-- artmaz); yalnızca ön ayar tablosu eklenir ve ilk ön ayarlarla doldurulur.
--
-- Geri alma planı: Down tabloları kaldırır; masalar ve girilmiş skorlar
-- kaybolur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE catalog_score_presets (
  key        text PRIMARY KEY,
  position   smallint NOT NULL,
  -- Dil kodu → ad; Türkçe zorunlu. Marka adı ön ayar adı olmaz.
  labels     jsonb NOT NULL,
  -- Kurallar (uygulama doğrular, internal/core/app/component/scoreboard):
  -- players_min, players_max, side, rounds, target, winner, result,
  -- best_of. Verilmeyen kural bileşenin parametresinden gelir.
  params     jsonb NOT NULL DEFAULT '{}',
  status     text NOT NULL DEFAULT 'active',
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT catalog_score_presets_key CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT catalog_score_presets_labels CHECK (
    jsonb_typeof(labels) = 'object' AND COALESCE(jsonb_typeof(labels -> 'tr') = 'string', false)),
  CONSTRAINT catalog_score_presets_params CHECK (jsonb_typeof(params) = 'object'),
  CONSTRAINT catalog_score_presets_status CHECK (status IN ('active', 'retired'))
);
CREATE TRIGGER catalog_score_presets_updated_at BEFORE UPDATE ON catalog_score_presets
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- İlk ön ayarlar (Notion "Faz 3 — Katalog ayrıntıları", ADR-0037 §5).
-- Kuralların ayrıntısı katalog işinde (#260) gözden geçirilir.
INSERT INTO catalog_score_presets (key, position, labels, params) VALUES
  ('batak', 1, '{"tr": "Batak", "en": "Batak"}',
   '{"players_min": 3, "players_max": 4, "side": "player", "rounds": "round", "winner": "high", "result": "points"}'),
  ('okey_101', 2, '{"tr": "101 Okey", "en": "101 Okey"}',
   '{"players_min": 2, "players_max": 4, "side": "player", "rounds": "round", "winner": "low", "result": "points"}'),
  ('okey', 3, '{"tr": "Okey", "en": "Okey"}',
   '{"players_min": 2, "players_max": 4, "side": "player", "rounds": "round", "winner": "high", "result": "points"}'),
  ('backgammon', 4, '{"tr": "Tavla", "en": "Backgammon"}',
   '{"players_min": 2, "players_max": 2, "side": "player", "rounds": "first_to", "target": 5, "winner": "first", "result": "points"}'),
  ('pisti', 5, '{"tr": "Pişti", "en": "Pishti"}',
   '{"players_min": 2, "players_max": 4, "side": "player", "rounds": "round", "target": 151, "winner": "high", "result": "points"}'),
  ('foosball', 6, '{"tr": "Langırt", "en": "Foosball"}',
   '{"players_min": 2, "players_max": 2, "rounds": "first_to", "target": 10, "winner": "first", "result": "points"}'),
  ('team_rounds', 7, '{"tr": "Takımlı tur oyunu", "en": "Team round game"}',
   '{"players_min": 2, "players_max": 10, "side": "team", "rounds": "round", "winner": "high", "result": "points"}'),
  ('free', 8, '{"tr": "Serbest", "en": "Free"}', '{}');

CREATE TABLE event_score_tables (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'scoreboard').
  component_key text NOT NULL,
  label         text NOT NULL,
  -- Masanın ön ayarı; ön ayarsız bileşende NULL. Emekliye ayrılan ön ayar
  -- silinmez, masa kurallarını kendisi taşır.
  preset_key    text REFERENCES catalog_score_presets (key),
  -- Masa kurulurken çözülen kurallar.
  side          text NOT NULL,
  rounds        text NOT NULL,
  winner        text NOT NULL,
  result        text NOT NULL,
  target        integer,
  best_of       smallint,
  -- Takım yazbozunda yerlerin geldiği takım bileşeninin anahtarı.
  team_key      text,
  status        text NOT NULL DEFAULT 'open',
  finished_at   timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_score_tables_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_score_tables_label CHECK (
    char_length(label) BETWEEN 1 AND 30 AND label = btrim(label) AND label !~ '[[:cntrl:]]'),
  CONSTRAINT event_score_tables_side CHECK (
    (side = 'player' AND team_key IS NULL)
    OR (side = 'team' AND COALESCE(team_key ~ '^[a-z][a-z0-9_]{1,39}$', false))),
  CONSTRAINT event_score_tables_rounds CHECK (rounds IN ('round', 'single', 'first_to')),
  CONSTRAINT event_score_tables_winner CHECK (winner IN ('high', 'low', 'first')),
  CONSTRAINT event_score_tables_result CHECK (result IN ('points', 'wdl')),
  CONSTRAINT event_score_tables_target CHECK (target IS NULL OR target BETWEEN 1 AND 100000),
  -- İlk N ve "ilk ulaşan kazanır" hedef puan ister.
  CONSTRAINT event_score_tables_target_needed CHECK (
    target IS NOT NULL OR (rounds <> 'first_to' AND winner <> 'first')),
  CONSTRAINT event_score_tables_best_of CHECK (best_of IS NULL OR best_of IN (3, 5)),
  CONSTRAINT event_score_tables_status CHECK (
    (status = 'open' AND finished_at IS NULL) OR (status = 'finished' AND finished_at IS NOT NULL)),
  CONSTRAINT event_score_tables_ref UNIQUE (id, event_id, component_key)
);
CREATE INDEX event_score_tables_event_idx ON event_score_tables (event_id, component_key);
CREATE TRIGGER event_score_tables_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_score_tables
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('scoreboard');

CREATE TABLE event_score_players (
  id            uuid PRIMARY KEY,
  table_id      uuid NOT NULL,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  position      smallint NOT NULL,
  -- Kişi; hesabı silinince NULL (yer "Silinmiş kullanıcı" olarak kalır).
  user_id       uuid,
  -- Takım yazbozunda takım bileşeninin anahtarı ve takım; takım silinince
  -- team_id NULL olur.
  team_key      text,
  team_id       uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_score_players_position CHECK (position BETWEEN 1 AND 50),
  CONSTRAINT event_score_players_one CHECK (team_key IS NULL OR user_id IS NULL),
  CONSTRAINT event_score_players_team_ref CHECK (team_id IS NULL OR team_key IS NOT NULL),
  CONSTRAINT event_score_players_position_uniq UNIQUE (table_id, position),
  CONSTRAINT event_score_players_table FOREIGN KEY (table_id, event_id, component_key)
    REFERENCES event_score_tables (id, event_id, component_key) ON DELETE CASCADE,
  CONSTRAINT event_score_players_team FOREIGN KEY (team_id, event_id, team_key)
    REFERENCES event_teams (id, event_id, component_key) ON DELETE SET NULL (team_id)
);
CREATE UNIQUE INDEX event_score_players_user_uniq ON event_score_players (table_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX event_score_players_team_uniq ON event_score_players (table_id, team_id) WHERE team_id IS NOT NULL;
-- Hesap silme ve dışa aktarma kişinin yerlerini arar.
CREATE INDEX event_score_players_user_idx ON event_score_players (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX event_score_players_team_idx ON event_score_players (team_id) WHERE team_id IS NOT NULL;
CREATE TRIGGER event_score_players_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_score_players
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('scoreboard');

CREATE TABLE event_scores (
  id            uuid PRIMARY KEY,
  table_id      uuid NOT NULL,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  player_id     uuid NOT NULL REFERENCES event_score_players (id) ON DELETE CASCADE,
  -- El (tur) numarası; tek maçta 1.
  round         smallint NOT NULL,
  -- `points` modunda puan (101 Okey'de eksi olabilir); `wdl` modunda
  -- sonuç.
  points        integer,
  outcome       text,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_scores_round CHECK (round BETWEEN 1 AND 200),
  CONSTRAINT event_scores_points CHECK (points IS NULL OR points BETWEEN -1000000 AND 1000000),
  CONSTRAINT event_scores_outcome CHECK (outcome IS NULL OR outcome IN ('win', 'draw', 'loss')),
  CONSTRAINT event_scores_one CHECK ((points IS NULL) <> (outcome IS NULL)),
  CONSTRAINT event_scores_round_uniq UNIQUE (player_id, round),
  CONSTRAINT event_scores_table FOREIGN KEY (table_id, event_id, component_key)
    REFERENCES event_score_tables (id, event_id, component_key) ON DELETE CASCADE
);
CREATE INDEX event_scores_table_idx ON event_scores (table_id, round);
CREATE TRIGGER event_scores_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_scores
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('scoreboard');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016). Bir
-- masa, yer ya da skor başka etkinliğe, bileşene ya da masaya geçemez:
-- UPDATE yalnızca değişebilen sütunlarda verilir. Hesap silme kişinin
-- yerini boşaltır (user_id). SELECT ve INSERT burada da açıkça verilir:
-- geri alınıp yeniden uygulanan migration'ı başka bir rol çalıştırırsa
-- varsayılan yetkiler oluşmaz. Ön ayarlar migration'la (ileride platform
-- paneliyle) değişir.
REVOKE INSERT ON catalog_score_presets FROM core_app;
GRANT SELECT ON catalog_score_presets TO core_app;
GRANT SELECT, INSERT, DELETE ON event_score_tables TO core_app;
GRANT UPDATE (label, status, finished_at, updated_at) ON event_score_tables TO core_app;
GRANT SELECT, INSERT, DELETE ON event_score_players TO core_app;
GRANT UPDATE (user_id) ON event_score_players TO core_app;
-- Skor değişmez: düzeltme eli silip yeniden yazar.
GRANT SELECT, INSERT, DELETE ON event_scores TO core_app;

-- +goose Down

DROP TABLE event_scores;
DROP TABLE event_score_players;
DROP TABLE event_score_tables;
DROP TABLE catalog_score_presets;
