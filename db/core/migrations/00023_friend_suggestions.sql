-- Arkadaşlık puanı ve arkadaş önerisi (issue #208, ADR-0028; Notion "Faz 2
-- (v2) — Medya ve Sosyal" §4, platform sahibi 2026-11-15).
--
-- friend_scores: iki kullanıcının son 12 ayda check-in ile doğrulanmış
--   ortak katılımından türeyen gizli puan. Satır yalnızca en az bir ortak
--   katılımı olan ve ikisinin de profillemesi açık olan çift içindir;
--   friend-suggestions arka plan işi tabloyu her gün tek transaction'da
--   baştan yazar. Puan hiçbir API cevabına, olaya, log'a girmez; yalnızca
--   öneri adaylarının sırasıdır. user_low_id < user_high_id (friendships
--   ile aynı sıra).
-- friend_suggestions: kullanıcıya (user_id) yapılan öneri. dismissed_at
--   doluysa "Kapat" denmiştir; o çift bir daha hiçbir yönde önerilmez.
--   Engellemede engelleyen adına kapatılmış satır yazılır. Açık satır 30
--   gün listelenir, 365 gün sonra silinir; kapatılmış satır hesap silinene
--   kadar kalır. source: suggested (iş önerdi) ya da blocked (engellemede
--   yazılan, baştan kapatılmış kayıt; listelenmez, haftalık sınıra
--   sayılmaz). Haftada en fazla bir öneri source = suggested satırlarından
--   sayılır.
-- social_settings: "Profilleme" (profiling_enabled, varsayılan açık) ve
--   "Beni önerme" (hide_from_suggestions, varsayılan kapalı). Satır yalnızca
--   kişi bir ayarı değiştirince açılır; satır yoksa varsayılanlar geçerlidir.
--
-- Yaş ve doğum tarihi burada yoktur ve hiçbir yerde tutulmaz: 18 yaş
-- sınırı öneri anında identity'nin IsMinor cevabıyla (evet/hayır)
-- denetlenir, cevap saklanmaz (ADR-0012, ADR-0028).
--
-- Kullanıcı kimlikleri identity'dedir; veritabanları arası foreign key
-- yoktur (ADR-0002). Hesap silinince user.deleted tüketicisi bu satırları
-- siler.
--
-- Geri alma planı: Down tabloları kaldırır; öneriler, kapatma kararları ve
-- ayarlar kaybolur (puan zaten türetilmiş veridir).

-- +goose Up
CREATE TABLE friend_scores (
  user_low_id    uuid NOT NULL,
  user_high_id   uuid NOT NULL,
  score          double precision NOT NULL,
  shared_count   integer NOT NULL,
  last_shared_at timestamptz NOT NULL,
  computed_at    timestamptz NOT NULL,

  PRIMARY KEY (user_low_id, user_high_id),
  CONSTRAINT friend_scores_order CHECK (user_low_id < user_high_id),
  CONSTRAINT friend_scores_score CHECK (score > 0),
  CONSTRAINT friend_scores_shared_count CHECK (shared_count >= 1)
);
-- Kişinin puanları her iki kolondan silinir (profilleme kapatma, hesap silme).
CREATE INDEX friend_scores_user_high_idx ON friend_scores (user_high_id);

CREATE TABLE friend_suggestions (
  user_id           uuid NOT NULL,
  suggested_user_id uuid NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  dismissed_at      timestamptz,
  source            text NOT NULL DEFAULT 'suggested',

  PRIMARY KEY (user_id, suggested_user_id),
  CONSTRAINT friend_suggestions_source CHECK (source IN ('suggested', 'blocked')),
  CONSTRAINT friend_suggestions_blocked_dismissed CHECK (source <> 'blocked' OR dismissed_at IS NOT NULL),
  CONSTRAINT friend_suggestions_not_self CHECK (user_id <> suggested_user_id),
  CONSTRAINT friend_suggestions_dismissed CHECK (dismissed_at IS NULL OR dismissed_at >= created_at)
);
-- Haftalık sınır ve liste: alıcının son önerileri.
CREATE INDEX friend_suggestions_user_created_idx ON friend_suggestions (user_id, created_at DESC);
-- "Beni önerme", profilleme kapatma, hesap silme ve çift düzeyinde kapatma.
CREATE INDEX friend_suggestions_suggested_idx ON friend_suggestions (suggested_user_id);
-- Saklama: 365 günden eski açık satırlar.
CREATE INDEX friend_suggestions_open_created_idx ON friend_suggestions (created_at) WHERE dismissed_at IS NULL;

CREATE TABLE social_settings (
  user_id               uuid PRIMARY KEY,
  profiling_enabled     boolean NOT NULL DEFAULT true,
  hide_from_suggestions boolean NOT NULL DEFAULT false,
  updated_at            timestamptz NOT NULL DEFAULT now()
);

-- core_app (ADR-0015): varsayılan yetki (00016) SELECT ve INSERT'tir.
-- friend_scores: günlük hesaplama tabloyu siler ve yeniden yazar; satır
--   güncellenmez.
-- friend_suggestions: yalnızca kapatma zamanı güncellenir (alıcı ve
--   önerilen kişi değişmez); silme profilleme, engel, saklama ve hesap
--   silmede.
-- social_settings: iki ayar ve zamanı güncellenir; satır hesap silmede
--   silinir.
GRANT DELETE ON friend_scores TO core_app;
GRANT UPDATE (dismissed_at), DELETE ON friend_suggestions TO core_app;
GRANT UPDATE (profiling_enabled, hide_from_suggestions, updated_at), DELETE ON social_settings TO core_app;

-- +goose Down
DROP TABLE social_settings;
DROP TABLE friend_suggestions;
DROP TABLE friend_scores;
