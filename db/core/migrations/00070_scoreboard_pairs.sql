-- Yazbozda eşli kip (issue #343; ADR-0044, ADR-0037 §5, ADR-0042).
--
-- Platform sahibinin kararı (2026-11-24): masada 4 kişi oturur, karşılıklı
-- oturanlar eştir (koltuk 1–3 ve 2–4), puan eş başına girilir; takım
-- bileşeni gerekmez. Eşli seçeneği olan oyunlar: Batak, 101 Okey, Okey,
-- Pişti. Bireysel kip kalır.
--
--   event_score_tables     taraf (side) üçüncü değeri alır: 'pair'. Eşli
--                          masa kişi masası gibi takım anahtarı taşımaz.
--   event_score_players    eşli masada yer numarası 1–4'tür ve takım yeri
--                          olmaz (tetikleyici). Tam 4 yer kuralı satır
--                          kısıtıyla ifade edilemez, uygulamadadır. Eş
--                          numarası konumdan türetilir, saklanmaz.
--   catalog_score_presets  dört eşli ön ayar; her biri bireyselinin hemen
--                          arkasında listelensin diye ön ayar sıraları
--                          10'un katlarına taşınır.
--   board_game_night       sürüm 2: yazboz eşli ön ayarları da sunar.
--                          Sürüm 1 satırı değişmez; o sürümle açılmış
--                          etkinlikler onunla kalır (ADR-0025 §2). Türün
--                          sürümü yalnızca ileri gider (00067 tetikleyicisi).
--
-- Katalog bir kez değiştiği için catalog_meta.catalog_version bir artar;
-- catalog.changed olayını catalog-publish işi yayınlar (ADR-0042 §5).
--
-- Up idempotenttir: ön ayarlar ve sürüm satırı ON CONFLICT DO NOTHING ile
-- eklenir, tür yalnızca sürüm 1'deyse sürüm 2'ye geçer; yeniden çalıştırmak
-- yalnızca katalog sürümünü artırır.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down, sürümün geri gitmesini engelleyen tetikleyiciyi
-- (00067) yalnızca kendi transaction'ında kapatıp türü sürüm 1'e döndürür
-- ve ikinci sürüm satırını siler; sürüm 2 ile açılmış etkinlik varsa geri
-- alma foreign key'de durur (o etkinlikler önce elle ele alınmalıdır).
-- Ardından eşli masaları yerleri ve skorlarıyla SİLER (VERİ SİLER),
-- tetikleyiciyi ve CHECK'i eski haline getirir, eşli ön ayarları siler ve
-- sıraları geri alır; catalog_version yine bir artar. Uygulama kodu da
-- birlikte geri alınmalıdır (eski kod 'pair' tarafını tanımaz).

-- +goose Up

ALTER TABLE event_score_tables DROP CONSTRAINT event_score_tables_side;
ALTER TABLE event_score_tables ADD CONSTRAINT event_score_tables_side CHECK (
  (side IN ('player', 'pair') AND team_key IS NULL)
  OR (side = 'team' AND COALESCE(team_key ~ '^[a-z][a-z0-9_]{1,39}$', false)));

-- +goose StatementBegin
CREATE FUNCTION event_score_players_pair_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (NEW.position > 4 OR NEW.team_key IS NOT NULL)
     AND EXISTS (SELECT 1 FROM event_score_tables WHERE id = NEW.table_id AND side = 'pair') THEN
    RAISE EXCEPTION 'eşli masada en çok 4 kişi yeri olur'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_score_players_pair';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER event_score_players_pair_guard
  BEFORE INSERT OR UPDATE OF table_id, position, team_key ON event_score_players
  FOR EACH ROW EXECUTE FUNCTION event_score_players_pair_guard();

UPDATE catalog_score_presets AS p SET position = v.position
FROM (VALUES ('batak', 10), ('okey_101', 20), ('okey', 30), ('backgammon', 40), ('pisti', 50),
             ('foosball', 60), ('team_rounds', 70), ('free', 80)) AS v (key, position)
WHERE p.key = v.key AND p.position < 10;

INSERT INTO catalog_score_presets (key, position, labels, params) VALUES
  ('batak_pairs', 11, '{"tr": "Batak (eşli)", "en": "Batak (pairs)"}',
   '{"players_min": 4, "players_max": 4, "side": "pair", "rounds": "round", "winner": "high", "result": "points"}'),
  ('okey_101_pairs', 21, '{"tr": "101 Okey (eşli)", "en": "101 Okey (pairs)"}',
   '{"players_min": 4, "players_max": 4, "side": "pair", "rounds": "round", "winner": "low", "result": "points"}'),
  ('okey_pairs', 31, '{"tr": "Okey (eşli)", "en": "Okey (pairs)"}',
   '{"players_min": 4, "players_max": 4, "side": "pair", "rounds": "round", "winner": "high", "result": "points"}'),
  ('pisti_pairs', 51, '{"tr": "Pişti (eşli)", "en": "Pishti (pairs)"}',
   '{"players_min": 4, "players_max": 4, "side": "pair", "rounds": "round", "target": 151, "winner": "high", "result": "points"}')
ON CONFLICT (key) DO NOTHING;

INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition) VALUES
  ('board_game_night', 2, '{"tr": "Masa oyunu gecesi"}', 'casino', ARRAY['in_person'],
   '{"components": [{"kind": "scoreboard", "params": {"tables": true, "presets": ["batak", "batak_pairs", "okey_101", "okey_101_pairs", "okey", "okey_pairs", "backgammon", "pisti", "pisti_pairs", "foosball", "free"], "guests": true}}, {"kind": "timer", "params": {"mode": {"organizer_choice": ["countdown", "stopwatch"], "default": "countdown"}}}, {"kind": "celebration", "params": {"source": "scoreboard"}}]}')
ON CONFLICT (type_key, version) DO NOTHING;

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 2 AND t.version = 1 AND t.key = 'board_game_night';

UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- +goose Down

-- Sürüm yalnızca bu geri almada, bu transaction içinde geri gider.
ALTER TABLE catalog_event_types DISABLE TRIGGER catalog_event_types_version_forward;

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 1 AND t.version = 2 AND t.key = 'board_game_night';

-- Türün güncel sürümüne bağlı ertelenmiş kontrol (00030) şimdi çalışır:
-- bekleyen tetikleyici olayı varken tablo değiştirilemez (ALTER TABLE).
SET CONSTRAINTS catalog_event_types_current_version_fkey IMMEDIATE;

ALTER TABLE catalog_event_types ENABLE TRIGGER catalog_event_types_version_forward;

DELETE FROM catalog_event_type_versions WHERE version = 2 AND type_key = 'board_game_night';

-- Eşli masalar yerleri ve skorlarıyla (ON DELETE CASCADE) silinir; masaya
-- kapsamlanmış hakem atamaları da.
DELETE FROM event_referees WHERE scope_id IN (SELECT id FROM event_score_tables WHERE side = 'pair');
DELETE FROM event_score_tables WHERE side = 'pair';

DROP TRIGGER event_score_players_pair_guard ON event_score_players;
DROP FUNCTION event_score_players_pair_guard();

ALTER TABLE event_score_tables DROP CONSTRAINT event_score_tables_side;
ALTER TABLE event_score_tables ADD CONSTRAINT event_score_tables_side CHECK (
  (side = 'player' AND team_key IS NULL)
  OR (side = 'team' AND COALESCE(team_key ~ '^[a-z][a-z0-9_]{1,39}$', false)));

DELETE FROM catalog_score_presets WHERE key IN ('batak_pairs', 'okey_101_pairs', 'okey_pairs', 'pisti_pairs');

UPDATE catalog_score_presets AS p SET position = v.position
FROM (VALUES ('batak', 1), ('okey_101', 2), ('okey', 3), ('backgammon', 4), ('pisti', 5),
             ('foosball', 6), ('team_rounds', 7), ('free', 8)) AS v (key, position)
WHERE p.key = v.key;

UPDATE catalog_meta SET catalog_version = catalog_version + 1;
