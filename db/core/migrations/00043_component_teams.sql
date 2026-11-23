-- Takım kurma bileşeni (`teams`; issue #248; ADR-0025 §4, §5).
--
--   event_teams         bir etkinliğin bir takım bileşenindeki takımlar.
--                       Takım sırasıyla (position, 1–50) tanınır; yeniden
--                       kurulan takımlar aynı sıradaki satırı korur (kimliği
--                       değişmez), fazlası silinir. Ad isteğe bağlıdır
--                       (1–40); adsız takımı istemci sırasıyla adlandırır.
--   event_team_members  takımlardaki kişiler. Kişi bir bileşende en fazla bir
--                       takımdadır (birincil anahtar). Takım silinince üyeleri
--                       de silinir; üye satırı takımın etkinliğine ve
--                       bileşenine bağlıdır (bileşik yabancı anahtar).
--
-- Takımları organizatör (ve moderatör, yönetici) kurar: rastgele, dengeli
-- (organizatörün o an verdiği seviyelerle; seviyeler hiçbir yerde
-- saklanmaz) ya da elle. Kurulan her şey kişinin etkinlikteki katılımıdır:
-- seri örneğine kopyalanmaz; hesap silmede kişinin satırı silinir, dışa
-- aktarmaya girer (takım sırası ve kimliği).
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('teams'),
-- 00033) bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları kaldırır; kurulmuş takımlar kaybolur.
-- Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_teams (
  id            uuid PRIMARY KEY,
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'teams').
  component_key text NOT NULL,
  position      smallint NOT NULL,
  name          text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Takımın son kurulduğu an; uygulama yazar (isteğin zamanı).
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_teams_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_teams_position CHECK (position BETWEEN 1 AND 50),
  CONSTRAINT event_teams_name_len CHECK (name IS NULL OR char_length(name) BETWEEN 1 AND 40),
  CONSTRAINT event_teams_position_uniq UNIQUE (event_id, component_key, position),
  -- Üye satırının bileşik yabancı anahtarı için.
  CONSTRAINT event_teams_scope_uniq UNIQUE (id, event_id, component_key)
);
CREATE TRIGGER event_teams_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_teams
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('teams');

CREATE TABLE event_team_members (
  event_id      uuid NOT NULL,
  component_key text NOT NULL,
  team_id       uuid NOT NULL,
  user_id       uuid NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (event_id, component_key, user_id),
  CONSTRAINT event_team_members_team FOREIGN KEY (team_id, event_id, component_key)
    REFERENCES event_teams (id, event_id, component_key) ON DELETE CASCADE
);
CREATE INDEX event_team_members_team_idx ON event_team_members (team_id, created_at, user_id);
-- Hesap silme ve dışa aktarma kişinin satırlarını arar.
CREATE INDEX event_team_members_user_idx ON event_team_members (user_id);
CREATE TRIGGER event_team_members_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_team_members
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('teams');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): takım
-- yalnızca adı değişir (yeniden kurulduğunda) ve silinir; üye satırı hiç
-- değişmez, silinip yeniden yazılır. Satırlar başka etkinliğe, bileşene ya
-- da kişiye taşınamaz. SELECT ve INSERT burada da açıkça verilir: geri
-- alınıp yeniden uygulanan migration'ı başka bir rol çalıştırırsa
-- varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_teams TO core_app;
GRANT UPDATE (name, updated_at) ON event_teams TO core_app;
GRANT SELECT, INSERT, DELETE ON event_team_members TO core_app;

-- +goose Down

DROP TABLE event_team_members;
DROP TABLE event_teams;
