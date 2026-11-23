-- Misafir oyuncu (issue #252; ADR-0037 §6).
--
--   event_guest_players  uygulamada hesabı olmayan oyuncu: yalnızca takma ad
--                        (1–24 karakter). Etkinliğe aittir; etkinlik başına
--                        en fazla 30 (uygulama, etkinlik kilitliyken sayar).
--                        Profil, kişisel kayıt ve iletişim alanı yoktur;
--                        hiçbir istatistiğe, kutlama bildirimine ve
--                        arkadaşlık puanına girmez.
--
-- Misafiri yalnızca organizatör ve hakem ekler; ekleme yazboz bileşeni
-- (`guests: true`) üzerinden yapılır. Misafir yazbozda bir yer
-- (event_score_players.guest_id) ve katılımcı kurasında bir yer
-- (event_draw_entries.guest_id) olabilir; eşleşme ağacının tarafları kura
-- yeri olduğu için ağaç misafirle değişmeden çalışır.
--
-- Takma ad içeriktir ve onu ekleyen kişinindir: etkinliğin bitiminden 30 gün
-- sonra arka plan işi takma adı siler (nickname NULL), ekleyen hesabını
-- silerse takma ad ve ekleyen hemen boşalır. Satır kalır: skor ve kura yeri
-- "Misafir" olarak görünür. Takma ad log'a, denetim kaydına ve olaya
-- yazılmaz.
--
-- Veri taşınmaz ve silinmez; katalog değişmez. event_draw_entries_fixed
-- fonksiyonu guest_id'yi de sabitler.
--
-- Geri alma planı: Down sütunları ve tabloyu kaldırır, CHECK'leri ve kura
-- fonksiyonunu eski haline getirir; misafirler ve misafirlerin masa ve kura
-- yerleri kaybolur (Down misafir yerlerini önce siler). Uygulama kodu da
-- birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_guest_players (
  id         uuid PRIMARY KEY,
  event_id   uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Takma ad; süresi dolunca ya da ekleyen hesabını silince NULL.
  nickname   text,
  -- Ekleyen (organizatör ya da hakem); hesabı silinince NULL.
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_guest_players_nickname CHECK (
    nickname IS NULL
    OR (char_length(nickname) BETWEEN 1 AND 24 AND nickname = btrim(nickname) AND nickname !~ '[[:cntrl:]]')),
  CONSTRAINT event_guest_players_ref UNIQUE (id, event_id)
);
CREATE INDEX event_guest_players_event_idx ON event_guest_players (event_id);
-- Hesap silme ve dışa aktarma ekleyenin satırlarını arar.
CREATE INDEX event_guest_players_creator_idx ON event_guest_players (created_by) WHERE created_by IS NOT NULL;
-- Süresi dolan takma adlar.
CREATE INDEX event_guest_players_nickname_idx ON event_guest_players (event_id) WHERE nickname IS NOT NULL;

-- Yazbozdaki yer: kişi, takım ya da misafir; en fazla biri.
ALTER TABLE event_score_players ADD COLUMN guest_id uuid;
ALTER TABLE event_score_players DROP CONSTRAINT event_score_players_one;
ALTER TABLE event_score_players ADD CONSTRAINT event_score_players_one
  CHECK (num_nonnulls(team_key, user_id, guest_id) <= 1);
ALTER TABLE event_score_players ADD CONSTRAINT event_score_players_guest
  FOREIGN KEY (guest_id, event_id) REFERENCES event_guest_players (id, event_id);
CREATE UNIQUE INDEX event_score_players_guest_uniq ON event_score_players (table_id, guest_id) WHERE guest_id IS NOT NULL;
CREATE INDEX event_score_players_guest_idx ON event_score_players (guest_id) WHERE guest_id IS NOT NULL;

-- Kuradaki yer: kişi, takım ya da misafir; en fazla biri.
ALTER TABLE event_draw_entries ADD COLUMN guest_id uuid;
ALTER TABLE event_draw_entries DROP CONSTRAINT event_draw_entries_one;
ALTER TABLE event_draw_entries ADD CONSTRAINT event_draw_entries_one
  CHECK (num_nonnulls(team_key, user_id, guest_id) <= 1);
ALTER TABLE event_draw_entries ADD CONSTRAINT event_draw_entries_guest
  FOREIGN KEY (guest_id, event_id) REFERENCES event_guest_players (id, event_id);
CREATE INDEX event_draw_entries_guest_idx ON event_draw_entries (guest_id) WHERE guest_id IS NOT NULL;

-- Kura sonucu değişmez: misafirin yeri de başka misafire geçemez ve
-- boşalmaz (misafir satırı silinmez; yalnızca takma adı silinir).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION event_draw_entries_fixed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.event_id IS DISTINCT FROM OLD.event_id
    OR NEW.component_key IS DISTINCT FROM OLD.component_key
    OR NEW.position IS DISTINCT FROM OLD.position
    OR NEW.team_key IS DISTINCT FROM OLD.team_key
    OR NEW.guest_id IS DISTINCT FROM OLD.guest_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR (NEW.user_id IS NOT NULL AND NEW.user_id IS DISTINCT FROM OLD.user_id)
    OR (NEW.team_id IS NOT NULL AND NEW.team_id IS DISTINCT FROM OLD.team_id) THEN
    RAISE EXCEPTION 'kura sonucu değiştirilemez'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_draw_entries_fixed';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016). Bir
-- misafir başka etkinliğe geçemez; yalnızca takma adı ve ekleyeni boşalır
-- (süre dolumu, hesap silme). Organizatör kullanılmayan misafiri siler.
-- SELECT ve INSERT burada da açıkça verilir: geri alınıp yeniden uygulanan
-- migration'ı başka bir rol çalıştırırsa varsayılan yetkiler oluşmaz.
-- Mevcut tabloların yeni sütunu tablo düzeyindeki INSERT ve SELECT
-- yetkisine girer; UPDATE verilmez.
GRANT SELECT, INSERT, DELETE ON event_guest_players TO core_app;
GRANT UPDATE (nickname, created_by) ON event_guest_players TO core_app;

-- +goose Down

DELETE FROM event_draw_entries WHERE guest_id IS NOT NULL;
DELETE FROM event_score_players WHERE guest_id IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION event_draw_entries_fixed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.event_id IS DISTINCT FROM OLD.event_id
    OR NEW.component_key IS DISTINCT FROM OLD.component_key
    OR NEW.position IS DISTINCT FROM OLD.position
    OR NEW.team_key IS DISTINCT FROM OLD.team_key
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR (NEW.user_id IS NOT NULL AND NEW.user_id IS DISTINCT FROM OLD.user_id)
    OR (NEW.team_id IS NOT NULL AND NEW.team_id IS DISTINCT FROM OLD.team_id) THEN
    RAISE EXCEPTION 'kura sonucu değiştirilemez'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_draw_entries_fixed';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE event_draw_entries DROP CONSTRAINT event_draw_entries_one;
ALTER TABLE event_draw_entries DROP COLUMN guest_id;
ALTER TABLE event_draw_entries ADD CONSTRAINT event_draw_entries_one CHECK (team_key IS NULL OR user_id IS NULL);

ALTER TABLE event_score_players DROP CONSTRAINT event_score_players_one;
ALTER TABLE event_score_players DROP COLUMN guest_id;
ALTER TABLE event_score_players ADD CONSTRAINT event_score_players_one CHECK (team_key IS NULL OR user_id IS NULL);

DROP TABLE event_guest_players;
