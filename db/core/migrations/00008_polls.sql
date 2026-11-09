-- Tayfa içi anket (veri modeli v14 "Anket ve Albüm (v12)" → "Anket (MVP)";
-- issue #60).
--
-- Anketi her aktif üye açar; susturulmuş üye açamaz ve oy veremez. İzinler
-- core'daki izin tablosundadır (internal/core/app/authz); oy kuralları
-- aşağıdaki trigger'la veritabanında da doğrulanır (ADR-0011, savunma
-- derinliği).
--
-- Gizli anket (anonymous): tekrar oyu önlemek için poll_votes.user_id yine
-- tutulur, ama kimin hangi seçeneğe oy verdiği veritabanından dışarı
-- çıkmaz: hiçbir API yanıtı, yönetici görünümü, bildirim, olay kaydı ya da
-- uygulama log'u bunu içermez. Kişiye yalnızca kendi oyu döner.
--
-- created_by ve deleted_by identity'nin kullanıcısıdır; veritabanları arası
-- foreign key yoktur (ADR-0009). Hesap silinince NULL olur; kişinin oyları
-- silinir.
--
-- Anket silmesi soft delete'tir; anketin oyları aynı transaction'da silinir
-- (geri alma yok, tutulmayan oy sızamaz). Kalıcı silme işi ayrıdır.

-- +goose Up

-- Etkinliğe bağlı anketin etkinliği aynı tayfada olmalıdır: bileşik foreign
-- key (polls_event_fkey) bunun için (tayfa_id, id) tekilliği ister.
ALTER TABLE events ADD CONSTRAINT events_tayfa_id_id_key UNIQUE (tayfa_id, id);

CREATE TABLE polls (
  id              uuid PRIMARY KEY,
  tayfa_id        uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- İsteğe bağlı (örn. "Kamp hangi hafta sonu?"). Etkinlik kalıcı silinince
  -- anket kalır, bağlantı boşalır.
  event_id        uuid,
  -- Anketi açan; hesabı silinirse NULL.
  created_by      uuid,
  question        text NOT NULL,
  anonymous       boolean NOT NULL DEFAULT false,
  multiple_choice boolean NOT NULL DEFAULT false,
  -- Planlanan kapanma zamanı; NULL: elle kapatılana kadar açık.
  closes_at       timestamptz,
  -- Erken kapatma zamanı.
  closed_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz,
  deleted_by      uuid,

  CONSTRAINT polls_question_len CHECK (char_length(question) BETWEEN 1 AND 200),
  CONSTRAINT polls_closes_at CHECK (closes_at IS NULL OR closes_at > created_at),
  CONSTRAINT polls_closed_at CHECK (closed_at IS NULL OR closed_at >= created_at),
  CONSTRAINT polls_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  -- Seçeneklerin bileşik foreign key'i için.
  CONSTRAINT polls_tayfa_id_id_key UNIQUE (tayfa_id, id),
  CONSTRAINT polls_event_fkey FOREIGN KEY (tayfa_id, event_id)
    REFERENCES events (tayfa_id, id) ON DELETE SET NULL (event_id)
);
-- Tayfanın anketleri, yeniden eskiye (v14 "İndeksler").
CREATE INDEX polls_tayfa_feed_idx ON polls (tayfa_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
-- Etkinliğin anketleri; foreign key kolonu da indekslenir.
CREATE INDEX polls_event_id_idx ON polls (tayfa_id, event_id, created_at DESC, id DESC) WHERE event_id IS NOT NULL;
-- Hesap silmede created_by'ı boşaltmak için.
CREATE INDEX polls_created_by_idx ON polls (created_by) WHERE created_by IS NOT NULL;
-- 30 günlük kalıcı silme işi.
CREATE INDEX polls_deleted_at_idx ON polls (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE poll_options (
  id       uuid PRIMARY KEY,
  poll_id  uuid NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
  text     text NOT NULL,
  -- 0'dan başlayan sıra; ankette en fazla 10 seçenek.
  position smallint NOT NULL,

  CONSTRAINT poll_options_text_len CHECK (char_length(text) BETWEEN 1 AND 100),
  CONSTRAINT poll_options_position CHECK (position BETWEEN 0 AND 9),
  CONSTRAINT poll_options_poll_position_key UNIQUE (poll_id, position),
  -- Oyların bileşik foreign key'i: oy yalnızca kendi anketinin seçeneğine
  -- verilir.
  CONSTRAINT poll_options_poll_id_id_key UNIQUE (poll_id, id)
);

CREATE TABLE poll_votes (
  poll_id    uuid NOT NULL,
  option_id  uuid NOT NULL,
  user_id    uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (poll_id, option_id, user_id),
  CONSTRAINT poll_votes_option_fkey FOREIGN KEY (poll_id, option_id)
    REFERENCES poll_options (poll_id, id) ON DELETE CASCADE
);
-- Kişinin bir anketteki oyu (v14 "İndeksler").
CREATE INDEX poll_votes_poll_id_user_id_idx ON poll_votes (poll_id, user_id);
-- Hesap silmede kişinin oylarını silmek için.
CREATE INDEX poll_votes_user_id_idx ON poll_votes (user_id);

-- Ankette 2–10 seçenek (v14). En fazla 10'u poll_options_position ile
-- tekillik sağlar; en az 2'yi bu ertelenmiş kısıt trigger'ı, anket ve
-- seçenekleri aynı transaction'da yazıldıktan sonra doğrular.
-- +goose StatementBegin
CREATE FUNCTION polls_check_options() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (SELECT count(*) FROM poll_options WHERE poll_id = NEW.id) < 2 THEN
    RAISE EXCEPTION 'ankette en az iki seçenek olmalı' USING ERRCODE = 'TF071';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER polls_check_options AFTER INSERT ON polls
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION polls_check_options();

-- Anketin kimlik alanları değişmez; gizlilik ve çoklu seçim ayarı ilk oydan
-- sonra değiştirilemez (v14: "Gizlilik ayarı ilk oydan sonra
-- değiştirilemez"). Seçenekler anket açıldıktan sonra değişmez.
-- +goose StatementBegin
CREATE FUNCTION polls_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tayfa_id IS DISTINCT FROM OLD.tayfa_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'anketin kimlik alanları değiştirilemez' USING ERRCODE = 'TF071';
  END IF;
  IF (NEW.anonymous IS DISTINCT FROM OLD.anonymous OR NEW.multiple_choice IS DISTINCT FROM OLD.multiple_choice)
     AND EXISTS (SELECT 1 FROM poll_votes WHERE poll_id = OLD.id) THEN
    RAISE EXCEPTION 'oy verilmiş anketin ayarı değiştirilemez' USING ERRCODE = 'TF071';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER polls_guard BEFORE UPDATE ON polls
  FOR EACH ROW EXECUTE FUNCTION polls_guard();

-- +goose StatementBegin
CREATE FUNCTION poll_options_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'anket seçenekleri değiştirilemez' USING ERRCODE = 'TF071';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER poll_options_guard BEFORE UPDATE ON poll_options
  FOR EACH ROW EXECUTE FUNCTION poll_options_guard();

-- Oy kuralları (v14 "Anket (MVP)"): anket görünür ve açık olmalı, oy veren
-- tayfanın susturulmamış aktif üyesi olmalı, tek seçimli ankette kişi başına
-- tek oy. Anket satırı kilitlenir; aynı kişinin eşzamanlı iki oyu sırayla
-- denetlenir. Oy değiştirmek, yeni oydan önce eski oyu siler (uygulama aynı
-- transaction'da yapar). Hata metni seçeneği ya da kişiyi içermez.
-- +goose StatementBegin
CREATE FUNCTION poll_votes_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  p polls%ROWTYPE;
BEGIN
  SELECT * INTO p FROM polls WHERE id = NEW.poll_id FOR NO KEY UPDATE;
  IF NOT FOUND OR p.deleted_at IS NOT NULL THEN
    RAISE EXCEPTION 'anket yok' USING ERRCODE = 'TF070';
  END IF;
  IF p.closed_at IS NOT NULL OR (p.closes_at IS NOT NULL AND p.closes_at <= now()) THEN
    RAISE EXCEPTION 'anket kapalı' USING ERRCODE = 'TF070';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM memberships m
    WHERE m.tayfa_id = p.tayfa_id AND m.user_id = NEW.user_id AND m.status = 'active'
      AND (m.muted_until IS NULL OR m.muted_until <= now())
  ) THEN
    RAISE EXCEPTION 'oy verme yetkisi yok' USING ERRCODE = 'TF070';
  END IF;
  IF NOT p.multiple_choice AND EXISTS (
    SELECT 1 FROM poll_votes WHERE poll_id = NEW.poll_id AND user_id = NEW.user_id
  ) THEN
    RAISE EXCEPTION 'tek seçimli ankette ikinci oy' USING ERRCODE = 'TF070';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER poll_votes_guard BEFORE INSERT ON poll_votes
  FOR EACH ROW EXECUTE FUNCTION poll_votes_guard();

-- +goose StatementBegin
CREATE FUNCTION poll_votes_no_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'oy değiştirilemez; eski oy silinip yenisi yazılır' USING ERRCODE = 'TF070';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER poll_votes_no_update BEFORE UPDATE ON poll_votes
  FOR EACH ROW EXECUTE FUNCTION poll_votes_no_update();

-- +goose Down
DROP TABLE poll_votes;
DROP TABLE poll_options;
DROP TABLE polls;
DROP FUNCTION poll_votes_no_update();
DROP FUNCTION poll_votes_guard();
DROP FUNCTION poll_options_guard();
DROP FUNCTION polls_guard();
DROP FUNCTION polls_check_options();
ALTER TABLE events DROP CONSTRAINT events_tayfa_id_id_key;
