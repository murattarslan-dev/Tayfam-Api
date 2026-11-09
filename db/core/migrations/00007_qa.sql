-- Soru-cevap (issue #59; veri modeli v14 "Soru-Cevap", "İndeksler",
-- "Uzunluk sınırları", "Otomatik Davranışlar", "Türkçe büyük/küçük harf",
-- "Silinen içeriği geri alma", "Çevrimdışı yazma"; Özellik Haritası:
-- faydalı oyu, etiketler, çözüm işaretleme yok, Türkçe tam metin arama).
--
-- Soruyu ve cevabı her aktif üye yazar (susturulmuş hariç); izinler core'daki
-- izin tablosundadır (internal/core/app/authz).
--
-- author_id ve deleted_by identity'nin kullanıcısıdır; veritabanları arası
-- foreign key yoktur (ADR-0009). Hesap silinince NULL olur ve istemci
-- "Silinmiş kullanıcı" gösterir; kişinin faydalı oyları silinir.
--
-- Silme soft delete'tir (deleted_at, deleted_by), duyurulardaki gibi:
--   Yazarın kendi silmesi: metin hemen boşaltılır, geri alınamaz.
--   Moderatör/yönetici silmesi: içerik gizlenir, 30 gün içinde yönetici geri
--     alabilir. Kalıcı silme işi ayrıdır.
--
-- Bütünlük: cevap, etiket bağı ve soru aynı tayfadadır. answers ve
-- question_tags tayfa_id'yi taşır ve (id, tayfa_id) üzerinden bileşik
-- foreign key ile bağlanır; başka tayfanın sorusuna cevap ya da etiket
-- veritabanında da yazılamaz (IDOR'a karşı ikinci savunma hattı).

-- +goose Up

-- Türkçe küçük harf (v14 "Türkçe büyük/küçük harf"). to_tsvector küçük
-- harfe çevirmeyi veritabanının libc yerel ayarıyla yapar: C.UTF-8'de
-- "IŞIK" "işik" olur, "ışık" ile eşleşmez; ICU tr-TR collation'ı bunu
-- değiştirmez. Arama metni bu yüzden önce Türkçe alfabeyle sabit bir
-- tabloya göre küçültülür (I → ı, İ → i). Fonksiyon IMMUTABLE'dır ve yerel
-- ayara bakmaz: üretilen kolonda ve sorguda aynı sonucu verir.
-- +goose StatementBegin
CREATE FUNCTION tr_lower(s text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
  SELECT translate(s, 'ABCÇDEFGĞHIİJKLMNOÖPQRSŞTUÜVWXYZÂÎÛ', 'abcçdefgğhıijklmnoöpqrsştuüvwxyzâîû')
$$;
-- +goose StatementEnd

CREATE TABLE questions (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  author_id         uuid,
  title             text NOT NULL,
  body              text NOT NULL DEFAULT '',
  -- Türkçe tam metin arama (v14 "Otomatik Davranışlar"). regconfig sabittir
  -- ('turkish'::regconfig), ifade IMMUTABLE'dır.
  search_vector     tsvector GENERATED ALWAYS AS (
    to_tsvector('turkish'::regconfig, tr_lower(title || ' ' || body))
  ) STORED,
  -- Aynı soru gönderiminin tekrarına karşı (v14 "Çevrimdışı yazma").
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  deleted_by        uuid,

  -- Görünen sorunun başlığı 10–150 karakterdir; yazarın kendi sildiği
  -- sorunun başlığı ve gövdesi boşaltılmıştır.
  CONSTRAINT questions_title_len CHECK (
    char_length(title) <= 150 AND (deleted_at IS NOT NULL OR char_length(title) >= 10)
  ),
  CONSTRAINT questions_body_len CHECK (char_length(body) <= 10000),
  CONSTRAINT questions_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  -- answers ve question_tags'in bileşik foreign key'i için.
  CONSTRAINT questions_id_tayfa_key UNIQUE (id, tayfa_id)
);
CREATE UNIQUE INDEX questions_client_request_key ON questions (author_id, client_request_id);
CREATE INDEX questions_search_idx ON questions USING gin (search_vector) WHERE deleted_at IS NULL;
-- Son sorular (id sayfalama için).
CREATE INDEX questions_tayfa_recent_idx ON questions (tayfa_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX questions_author_id_idx ON questions (author_id) WHERE author_id IS NOT NULL;
CREATE INDEX questions_deleted_at_idx ON questions (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER questions_updated_at BEFORE UPDATE ON questions FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE answers (
  id                uuid PRIMARY KEY,
  question_id       uuid NOT NULL,
  tayfa_id          uuid NOT NULL,
  author_id         uuid,
  body              text NOT NULL,
  -- answer_votes'a ekleme/silme ile aynı transaction'da güncellenir (v14
  -- "Otomatik Davranışlar").
  vote_count        integer NOT NULL DEFAULT 0,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  deleted_by        uuid,

  CONSTRAINT answers_question_fkey FOREIGN KEY (question_id, tayfa_id)
    REFERENCES questions (id, tayfa_id) ON DELETE CASCADE,
  CONSTRAINT answers_body_len CHECK (
    char_length(body) <= 10000 AND (deleted_at IS NOT NULL OR char_length(body) >= 1)
  ),
  CONSTRAINT answers_vote_count CHECK (vote_count >= 0),
  CONSTRAINT answers_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
CREATE UNIQUE INDEX answers_client_request_key ON answers (author_id, client_request_id);
-- Oya göre sıralı cevaplar (v14 "İndeksler"); eşitlikte eskiden yeniye.
CREATE INDEX answers_question_votes_idx ON answers (question_id, vote_count DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX answers_author_id_idx ON answers (author_id) WHERE author_id IS NOT NULL;
CREATE INDEX answers_deleted_at_idx ON answers (deleted_at) WHERE deleted_at IS NOT NULL;
-- updated_at metnin değiştiği zamandır: oy sayısı değişince ilerlemez.
CREATE TRIGGER answers_updated_at BEFORE UPDATE OF body, deleted_at ON answers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Kişi başına tek faydalı oyu (v14). Kullanıcı → answer_votes CASCADE:
-- hesap silinince oylar silinir, vote_count aynı transaction'da düşer.
CREATE TABLE answer_votes (
  answer_id  uuid NOT NULL REFERENCES answers (id) ON DELETE CASCADE,
  user_id    uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (answer_id, user_id)
);
CREATE INDEX answer_votes_user_id_idx ON answer_votes (user_id);

-- Etiketler tayfaya aittir. name ilk yazıldığı haliyle görünür;
-- name_normalized Go'da strings.ToLowerSpecial(unicode.TurkishCase, …) ile
-- üretilir ve tekilliği sağlar ("Çadır" ile "ÇADIR" aynı etikettir).
CREATE TABLE tags (
  id              uuid PRIMARY KEY,
  tayfa_id        uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  name            text NOT NULL,
  name_normalized text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tags_name_len CHECK (char_length(name) BETWEEN 1 AND 30),
  CONSTRAINT tags_name_normalized_len CHECK (char_length(name_normalized) BETWEEN 1 AND 30),
  CONSTRAINT tags_id_tayfa_key UNIQUE (id, tayfa_id)
);
CREATE UNIQUE INDEX tags_tayfa_name_key ON tags (tayfa_id, name_normalized);

CREATE TABLE question_tags (
  question_id uuid NOT NULL,
  tag_id      uuid NOT NULL,
  tayfa_id    uuid NOT NULL,
  PRIMARY KEY (question_id, tag_id),
  CONSTRAINT question_tags_question_fkey FOREIGN KEY (question_id, tayfa_id)
    REFERENCES questions (id, tayfa_id) ON DELETE CASCADE,
  CONSTRAINT question_tags_tag_fkey FOREIGN KEY (tag_id, tayfa_id)
    REFERENCES tags (id, tayfa_id) ON DELETE CASCADE
);
-- Etikete göre filtre.
CREATE INDEX question_tags_tag_id_idx ON question_tags (tag_id);

-- +goose Down
DROP TABLE question_tags;
DROP TABLE tags;
DROP TABLE answer_votes;
DROP TABLE answers;
DROP TABLE questions;
DROP FUNCTION tr_lower(text);
