-- Oylama bileşeni (`vote`; issue #254; ADR-0025 §4, §5; ADR-0037 §3).
--
-- Seçenek kaynağı tanımdaki `source` parametresidir (platform sahibi,
-- 2026-11-23: `route_stops` kaldırıldı):
--
--   free          organizatörün yazdığı seçenekler (event_vote_options)
--   participants  etkinliğin katılımcıları ("geliyorum" ya da katılımı
--                 doğrulanmış etkin üyeler); seçenek saklanmaz
--   vehicles      aynı türdeki araçlar bileşenine yazılmış araçlar
--                 (event_vehicles); seçenek saklanmaz
--   album         etkinliğin albümündeki silinmemiş fotoğraflar
--                 (event_photos); seçenek saklanmaz
--
--   event_vote_options   serbest seçenekler (en fazla 20; etiket 1–100
--                        karakter, Türkçe küçük harfle tekil).
--   event_votes          oylar: kişi başına bileşen başına bir oy. Oy
--                        gizlidir: kimin neye oy verdiği hiçbir yanıtta,
--                        denetim kaydında, olayda ve log'da yer almaz.
--                        Hedef kaynağa göre tam olarak bir kolondadır.
--   event_vote_closures  organizatörün oylamayı kapattığı an. Kapanış geri
--                        alınmaz; sonuç (seçenek başına oy) kapanınca ya da
--                        bileşen donunca görünür.
--
-- Tablolar (event_id, component_key) ile etkinliğe ve tür sürümündeki
-- bileşene bağlıdır; ortak tetikleyici (event_component_guard('vote'), 00033)
-- bileşeni olmayan ya da bileşeni kapalı etkinliğe yazmayı reddeder.
-- event_votes_target_guard ikinci savunma hattıdır: hedef kolonu bileşenin
-- kaynağına uyar, hedef aynı etkinliktedir ve oy veren kendine, kendi
-- aracına ya da kendi fotoğrafına oy veremez.
--
-- Araç ve fotoğraf hedefleri yabancı anahtar değil, tetikleyiciyle
-- denetlenir: 00035'in ve 00023'ün geri alınması (testlerde tek başına
-- çalıştırılır) bu tabloya bağlanmaz. Araç silinince ya da fotoğraf silinince
-- (soft delete dahil) ona verilen oylar silinir (event_vehicles_vote_cleanup,
-- event_photos_vote_cleanup).
--
-- Kişisel veri (ADR-0025 §11): oy kişiye aittir, dışa aktarmaya girer
-- (başka kişinin kimliği olmadan) ve hesap silmede silinir; kişiye, aracına
-- ya da fotoğrafına verilen oylar da silinir. Seçeneği yazan ve kapatan
-- boşalır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (bugünkü türlerde oylama
-- bileşeni yok).
--
-- Geri alma planı: Down tabloları, tetikleyicileri ve fonksiyonları
-- kaldırır; seçenekler, oylar ve kapanışlar kaybolur. Oylama kaynaklı
-- kutlamalar kazanansız kalır. Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE event_vote_options (
  id               uuid PRIMARY KEY,
  event_id         uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  -- Tür tanımındaki bileşenin anahtarı (key; yoksa kind = 'vote').
  component_key    text NOT NULL,
  -- Kırpılmış, iç boşlukları teke indirilmiş etiket; denetim karakteri yok.
  label            text NOT NULL,
  -- Tekillik için Türkçe küçük harf (strings.ToLowerSpecial).
  label_normalized text NOT NULL,
  -- Bileşendeki sıra (1'den; silinen seçenek boşluk bırakır).
  position         smallint NOT NULL,
  -- Seçeneği yazan yönetici. Hesap silinince boşaltılır.
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT event_vote_options_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_vote_options_label CHECK (
    char_length(label) BETWEEN 1 AND 100 AND label = btrim(label) AND label !~ '[[:cntrl:]]'),
  CONSTRAINT event_vote_options_label_normalized CHECK (char_length(label_normalized) BETWEEN 1 AND 200),
  CONSTRAINT event_vote_options_position CHECK (position BETWEEN 1 AND 1000),
  CONSTRAINT event_vote_options_label_key UNIQUE (event_id, component_key, label_normalized),
  -- Oyların bileşik foreign key'i için.
  CONSTRAINT event_vote_options_scope_key UNIQUE (id, event_id, component_key)
);
CREATE INDEX event_vote_options_event_idx ON event_vote_options (event_id, component_key, position);
-- Hesap silme.
CREATE INDEX event_vote_options_created_by_idx ON event_vote_options (created_by) WHERE created_by IS NOT NULL;
CREATE TRIGGER event_vote_options_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_vote_options
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('vote');

CREATE TABLE event_votes (
  id                uuid PRIMARY KEY,
  event_id          uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  component_key     text NOT NULL,
  -- Oy veren. Hesap silinince oyları silinir.
  user_id           uuid NOT NULL,
  -- Hedef: kaynağa göre tam olarak biri.
  option_id         uuid,
  -- participants: oy verilen katılımcı. Hesabı silinince ona verilen oylar
  -- silinir.
  candidate_user_id uuid,
  -- vehicles: oy verilen araç (event_vehicles.id).
  event_vehicle_id  uuid,
  -- album: oy verilen fotoğraf (event_photos.id).
  photo_id          uuid,
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL,

  CONSTRAINT event_votes_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT event_votes_voter_key UNIQUE (event_id, component_key, user_id),
  CONSTRAINT event_votes_one_target CHECK (num_nonnulls(option_id, candidate_user_id, event_vehicle_id, photo_id) = 1),
  CONSTRAINT event_votes_not_self CHECK (candidate_user_id IS NULL OR candidate_user_id <> user_id),
  CONSTRAINT event_votes_times CHECK (updated_at >= created_at),
  CONSTRAINT event_votes_option FOREIGN KEY (option_id, event_id, component_key)
    REFERENCES event_vote_options (id, event_id, component_key) ON DELETE CASCADE
);
-- Dışa aktarma ve hesap silme.
CREATE INDEX event_votes_user_id_idx ON event_votes (user_id);
CREATE INDEX event_votes_candidate_user_id_idx ON event_votes (candidate_user_id) WHERE candidate_user_id IS NOT NULL;
-- Seçeneğin silinmesi (yabancı anahtar), aracın ve fotoğrafın temizliği.
CREATE INDEX event_votes_option_idx ON event_votes (option_id) WHERE option_id IS NOT NULL;
CREATE INDEX event_votes_vehicle_idx ON event_votes (event_vehicle_id) WHERE event_vehicle_id IS NOT NULL;
CREATE INDEX event_votes_photo_idx ON event_votes (photo_id) WHERE photo_id IS NOT NULL;
CREATE TRIGGER event_votes_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_votes
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('vote');

-- Hedef denetimi (ikinci hat; uygulama aynı kuralları önce denetler).
-- Mesaj kişiyi ve hedefi adlandırmaz.
-- +goose StatementBegin
CREATE FUNCTION event_votes_target_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  src text;
  ok  boolean;
BEGIN
  src := event_component_definition(NEW.event_id, NEW.component_key) -> 'params' ->> 'source';
  ok := CASE src
    WHEN 'free' THEN NEW.option_id IS NOT NULL
    WHEN 'participants' THEN NEW.candidate_user_id IS NOT NULL
    WHEN 'vehicles' THEN NEW.event_vehicle_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM event_vehicles v
      WHERE v.id = NEW.event_vehicle_id AND v.event_id = NEW.event_id AND v.user_id <> NEW.user_id)
    WHEN 'album' THEN NEW.photo_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM event_photos p
      WHERE p.id = NEW.photo_id AND p.event_id = NEW.event_id AND p.deleted_at IS NULL AND p.uploader_id <> NEW.user_id)
    ELSE false
  END;
  IF NOT ok THEN
    RAISE EXCEPTION 'oyun hedefi oylamanın kaynağına uymuyor'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'event_votes_target_guard';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_votes_target_guard
  BEFORE INSERT OR UPDATE OF option_id, candidate_user_id, event_vehicle_id, photo_id ON event_votes
  FOR EACH ROW EXECUTE FUNCTION event_votes_target_guard();

-- Araç ya da fotoğraf gidince ona verilen oylar da gider.
-- +goose StatementBegin
CREATE FUNCTION event_vehicles_vote_cleanup() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM event_votes WHERE event_vehicle_id = OLD.id;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_vehicles_vote_cleanup AFTER DELETE ON event_vehicles
  FOR EACH ROW EXECUTE FUNCTION event_vehicles_vote_cleanup();

-- +goose StatementBegin
CREATE FUNCTION event_photos_vote_cleanup() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM event_votes WHERE photo_id = OLD.id;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER event_photos_vote_cleanup AFTER DELETE ON event_photos
  FOR EACH ROW EXECUTE FUNCTION event_photos_vote_cleanup();
CREATE TRIGGER event_photos_vote_cleanup_soft AFTER UPDATE OF deleted_at ON event_photos
  FOR EACH ROW WHEN (NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL)
  EXECUTE FUNCTION event_photos_vote_cleanup();

CREATE TABLE event_vote_closures (
  event_id      uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
  component_key text NOT NULL,
  -- Kapatan yönetici. Hesap silinince boşaltılır.
  closed_by     uuid,
  closed_at     timestamptz NOT NULL,

  PRIMARY KEY (event_id, component_key),
  CONSTRAINT event_vote_closures_key CHECK (component_key ~ '^[a-z][a-z0-9_]{1,39}$')
);
-- Hesap silme.
CREATE INDEX event_vote_closures_closed_by_idx ON event_vote_closures (closed_by) WHERE closed_by IS NOT NULL;
CREATE TRIGGER event_vote_closures_component_guard
  BEFORE INSERT OR UPDATE OF event_id, component_key ON event_vote_closures
  FOR EACH ROW EXECUTE FUNCTION event_component_guard('vote');

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016):
-- seçenek başka etkinliğe ya da bileşene taşınamaz, etiketi değişmez;
-- yalnızca silinir ve hesap silmede yazanı boşalır. Oy yeniden verilince
-- yalnızca hedefi ve zamanı değişir; geri alınınca silinir. Kapanış
-- silinmez; hesap silmede kapatan boşalır. SELECT ve INSERT burada da açıkça
-- verilir: geri alınıp yeniden uygulanan migration'ı başka bir rol
-- çalıştırırsa varsayılan yetkiler oluşmaz.
GRANT SELECT, INSERT, DELETE ON event_vote_options TO core_app;
GRANT UPDATE (created_by) ON event_vote_options TO core_app;
GRANT SELECT, INSERT, DELETE ON event_votes TO core_app;
GRANT UPDATE (option_id, candidate_user_id, event_vehicle_id, photo_id, updated_at) ON event_votes TO core_app;
GRANT SELECT, INSERT ON event_vote_closures TO core_app;
GRANT UPDATE (closed_by) ON event_vote_closures TO core_app;

-- +goose Down

DROP TABLE event_vote_closures;
DROP TRIGGER event_photos_vote_cleanup_soft ON event_photos;
DROP TRIGGER event_photos_vote_cleanup ON event_photos;
DROP FUNCTION event_photos_vote_cleanup();
DROP TRIGGER event_vehicles_vote_cleanup ON event_vehicles;
DROP FUNCTION event_vehicles_vote_cleanup();
DROP TABLE event_votes;
DROP FUNCTION event_votes_target_guard();
DROP TABLE event_vote_options;
