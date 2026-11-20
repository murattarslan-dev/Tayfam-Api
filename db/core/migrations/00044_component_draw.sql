-- Kura bileşeni (`draw`; issue #249; ADR-0025 §4, §5, §6).
--
--   event_draws         bir etkinliğin bir kura bileşeninde çekilen kura:
--                       neyin çekildiği (katılımcılar ya da bir takım
--                       bileşeninin takımları) ve ne zaman. Bileşen başına
--                       bir kez çekilir (birincil anahtar).
--   event_draw_entries  kuranın sırası: 1., 2., … yer. Ardışık iki yer bir
--                       eşleşmedir (1–2, 3–4 …); tek sayıda girişte son yer
--                       bay geçer. Eşleşme ağacı (#251) sırayı tohum olarak
--                       kullanır.
--
-- Kura sonucu sabittir ve denetlenebilir: core_app satırı yalnızca yazar,
-- değiştiremez ve silemez; yeniden çekim yoktur. Tek değişiklik geri
-- çekilmedir: hesabı silinen kişinin yeri (user_id) ve silinen takımın yeri
-- (team_id) boşalır, sıra aynı kalır (event_draw_entries_fixed). Kurayı
-- kimin çektiği denetim kaydındadır (event.draw_made); tabloda tutulmaz.
--
-- Rastgelelik uygulamada yalnızca crypto/rand ile üretilir (CLAUDE.md kesin
-- kural 4). Kişinin kuradaki yeri katılımıdır: hesap silmede boşalır, dışa
-- aktarmaya girer. Kura seri örneğine kopyalanmaz.
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('draw'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
-- Takım yeri, aynı etkinliğin takımına bileşik yabancı anahtarla bağlıdır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları ve tetikleyici fonksiyonunu kaldırır;
-- çekilmiş kuralar kaybolur. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_draws (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'draw').
  component_key text NOT NULL,
  -- participants: etkinliğin katılımcıları; teams: team_key takım
  -- bileşeninin takımları.
  subject       text NOT NULL,
  team_key      text,
  drawn_at      timestamptz NOT NULL,

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_draws_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_draws_subject CHECK (
    (subject = 'participants' AND team_key IS NULL)
    OR (subject = 'teams' AND team_key ~ '^[a-z][a-z0-9_]{1,39}$'))
);
CREATE TRIGGER event_draws_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_draws
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('draw');

CREATE TABLE event_draw_entries (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  position      smallint NOT NULL,
  -- Katılımcı kurasında kişi; hesabı silinince NULL (yer boş kalır).
  user_id       uuid,
  -- Takım kurasında takım bileşeninin anahtarı ve takım; takım silinince
  -- team_id NULL olur (yer boş kalır).
  team_key      text,
  team_id       uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_draw_entries_position CHECK (position BETWEEN 1 AND 500),
  -- Bir yer ya kişinin ya takımındır.
  CONSTRAINT event_draw_entries_one CHECK (team_key IS NULL OR user_id IS NULL),
  CONSTRAINT event_draw_entries_team_ref CHECK (team_id IS NULL OR team_key IS NOT NULL),
  CONSTRAINT event_draw_entries_position_uniq UNIQUE (event_id, component_key, position),
  CONSTRAINT event_draw_entries_draw FOREIGN KEY (event_id, component_key)
    REFERENCES event_draws (event_id, component_key) ON DELETE CASCADE,
  CONSTRAINT event_draw_entries_team FOREIGN KEY (team_id, event_id, team_key)
    REFERENCES event_teams (id, event_id, component_key) ON DELETE SET NULL (team_id)
);
-- Hesap silme ve dışa aktarma kişinin satırlarını arar.
CREATE INDEX event_draw_entries_user_idx ON event_draw_entries (user_id) WHERE user_id IS NOT NULL;
-- Takım silinince yerini boşaltmak için.
CREATE INDEX event_draw_entries_team_idx ON event_draw_entries (team_id) WHERE team_id IS NOT NULL;
CREATE TRIGGER event_draw_entries_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_draw_entries
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('draw');

-- Kura sonucu değişmez: bir yer yalnızca boşalabilir (kişi ya da takım
-- NULL olur); başka kişiye ya da takıma, başka sıraya, etkinliğe ya da
-- bileşene geçemez. Sahip rolü (migration, yabancı anahtar eylemi) için de
-- geçerlidir.
-- +goose StatementBegin
CREATE FUNCTION event_draw_entries_fixed() RETURNS trigger
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
CREATE TRIGGER event_draw_entries_fixed
  BEFORE UPDATE ON event_draw_entries
  FOR EACH ROW EXECUTE FUNCTION event_draw_entries_fixed();

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): kura
-- yalnızca yazılır; hesap silme kişinin yerini boşaltır (user_id). Satırlar
-- silinmez (etkinlikle birlikte gider). SELECT ve INSERT burada da açıkça
-- verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT ON event_draws TO core_app;
GRANT SELECT, INSERT ON event_draw_entries TO core_app;
GRANT UPDATE (user_id) ON event_draw_entries TO core_app;

-- +goose Down

DROP TABLE event_draw_entries;
DROP TABLE event_draws;
DROP FUNCTION event_draw_entries_fixed();
