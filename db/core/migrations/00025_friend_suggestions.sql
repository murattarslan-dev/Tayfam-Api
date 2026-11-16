-- Arkadaşlık puanı ve arkadaş önerisi (issue #208, ADR-0030; Notion "Faz 2
-- (v2) — Medya ve Sosyal" §4, platform sahibi 2026-11-15).
--
-- friend_scores: iki kullanıcının son 12 ayda check-in ile doğrulanmış
--   ortak katılımından türeyen gizli puan. Satır yalnızca en az bir ortak
--   katılımı olan, ikisinin de profillemesi açık ve aralarında engel
--   olmayan çift içindir; friend-suggestions arka plan işi tabloyu her gün
--   tek transaction'da baştan yazar. Puan hiçbir API cevabına, olaya, log'a
--   girmez; yalnızca sıralama sinyalidir. user_low_id < user_high_id
--   (friendships ile aynı sıra).
-- friend_suggestions: kullanıcıya (user_id) yapılan öneri ve çift
--   hafızası. Satır silinmez (yalnızca saklama süresi ve hesap silme):
--   aynı kişi aynı kişiye bir daha önerilmez, haftalık sınır da buradan
--   sayılır; ayar açıp kapatmak bunları sıfırlamaz.
--   - dismissed_at: "Kapat" ya da kalıcı dışlama; çift bir daha hiçbir
--     yönde önerilmez.
--   - withdrawn_at: öneri artık gösterilmez (profilleme kapandı, "Beni
--     önerme" açıldı, karşı taraf engelledi) ama hafıza ve haftalık sınır
--     için satır kalır. Kalıcı dışlama değildir.
--   - source: suggested (iş önerdi); blocked, declined, unfriended
--     (engelleme, isteği reddetme, arkadaşlığı bitirme anında yazılan,
--     baştan kapatılmış kayıt: öneri değildir, listelenmez, haftalık
--     sınıra sayılmaz, dışa aktarmaya öneri olarak girmez).
--   Açık öneri 30 gün listelenir; kapatılmamış satır 365 gün sonra silinir,
--   kapatılmış satır hesap silinene kadar kalır.
-- social_settings: "Profilleme" (profiling_enabled, varsayılan açık) ve
--   "Beni önerme" (hide_from_suggestions, varsayılan kapalı). Satır yalnızca
--   kişi bir ayarı değiştirince açılır; satır yoksa varsayılanlar geçerlidir.
-- friend_suggestion_allowed(alıcı, aday): bir çiftin önerilebilmesinin tek
--   tanımı. Aday sorgusu, öneriyi yazan sorgu ve liste aynı fonksiyonu
--   kullanır; kurallar üç yerde ayrı ayrı kaymaz.
--
-- Yaş ve doğum tarihi burada yoktur ve hiçbir yerde tutulmaz: 18 yaş
-- sınırı öneri anında identity'nin IsMinor cevabıyla (evet/hayır)
-- denetlenir, cevap saklanmaz (ADR-0012, ADR-0030).
--
-- Kullanıcı kimlikleri identity'dedir; veritabanları arası foreign key
-- yoktur (ADR-0002). Hesap silinince user.deleted tüketicisi bu satırları
-- siler.
--
-- Geri alma planı: Down fonksiyonu ve tabloları kaldırır; öneriler, kapatma
-- kararları ve ayarlar kaybolur (puan zaten türetilmiş veridir).

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
  withdrawn_at      timestamptz,
  source            text NOT NULL DEFAULT 'suggested',

  PRIMARY KEY (user_id, suggested_user_id),
  CONSTRAINT friend_suggestions_not_self CHECK (user_id <> suggested_user_id),
  CONSTRAINT friend_suggestions_source CHECK (source IN ('suggested', 'blocked', 'declined', 'unfriended')),
  CONSTRAINT friend_suggestions_marker_dismissed CHECK (source = 'suggested' OR dismissed_at IS NOT NULL),
  CONSTRAINT friend_suggestions_dismissed CHECK (dismissed_at IS NULL OR dismissed_at >= created_at),
  CONSTRAINT friend_suggestions_withdrawn CHECK (withdrawn_at IS NULL OR withdrawn_at >= created_at)
);
-- Haftalık sınır ve liste: alıcının son önerileri.
CREATE INDEX friend_suggestions_user_created_idx ON friend_suggestions (user_id, created_at DESC);
-- "Beni önerme", profilleme kapatma, hesap silme ve çift düzeyinde kapatma.
CREATE INDEX friend_suggestions_suggested_idx ON friend_suggestions (suggested_user_id);
-- Saklama: 365 günden eski kapatılmamış satırlar.
CREATE INDEX friend_suggestions_open_created_idx ON friend_suggestions (created_at) WHERE dismissed_at IS NULL;

CREATE TABLE social_settings (
  user_id               uuid PRIMARY KEY,
  profiling_enabled     boolean NOT NULL DEFAULT true,
  hide_from_suggestions boolean NOT NULL DEFAULT false,
  updated_at            timestamptz NOT NULL DEFAULT now()
);

-- b, a'ya önerilebilir mi (ADR-0030 §3): arkadaşlık ya da bekleyen istek
-- yok; iki yönde engel, şikâyet, reddedilmiş istek ve kapatma yok; ikisinin
-- de profillemesi açık, b "Beni önerme" demiyor; ikisi de silinmemiş ve
-- banlı değil. Yaş burada yoktur (identity'ye sorulur), haftalık sınır ve
-- "daha önce önerildi" alıcıya özgüdür ve sorgularda ayrıca denetlenir.
-- +goose StatementBegin
CREATE FUNCTION friend_suggestion_allowed(a uuid, b uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT a <> b
    AND NOT EXISTS (SELECT 1 FROM friendships f
                    WHERE f.user_low_id = LEAST(a, b) AND f.user_high_id = GREATEST(a, b))
    AND NOT EXISTS (SELECT 1 FROM user_blocks ub
                    WHERE (ub.blocker_id = a AND ub.blocked_id = b) OR (ub.blocker_id = b AND ub.blocked_id = a))
    AND NOT EXISTS (SELECT 1 FROM reports r
                    WHERE (r.reporter_id = a AND r.subject_user_id = b) OR (r.reporter_id = b AND r.subject_user_id = a))
    AND NOT EXISTS (SELECT 1 FROM friend_request_outcomes o
                    WHERE o.outcome = 'declined'
                      AND ((o.requester_id = a AND o.recipient_id = b) OR (o.requester_id = b AND o.recipient_id = a)))
    AND NOT EXISTS (SELECT 1 FROM friend_suggestions fs
                    WHERE fs.dismissed_at IS NOT NULL
                      AND ((fs.user_id = a AND fs.suggested_user_id = b) OR (fs.user_id = b AND fs.suggested_user_id = a)))
    AND NOT EXISTS (SELECT 1 FROM social_settings s
                    WHERE (s.user_id IN (a, b) AND NOT s.profiling_enabled) OR (s.user_id = b AND s.hide_from_suggestions))
    AND NOT EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id IN (a, b) AND p.deleted_at IS NOT NULL)
    AND NOT EXISTS (SELECT 1 FROM user_platform_bans x WHERE x.user_id IN (a, b) AND x.banned_at IS NOT NULL)
$$;
-- +goose StatementEnd

-- core_app (ADR-0015): varsayılan yetki (00016) SELECT ve INSERT'tir.
-- friend_scores: günlük hesaplama tabloyu siler ve yeniden yazar; satır
--   güncellenmez.
-- friend_suggestions: yalnızca kapatma ve geri çekme zamanı güncellenir
--   (alıcı, önerilen kişi ve kaynak değişmez); silme saklama ve hesap
--   silmede.
-- social_settings: iki ayar ve zamanı güncellenir; satır hesap silmede
--   silinir.
GRANT DELETE ON friend_scores TO core_app;
GRANT UPDATE (dismissed_at, withdrawn_at), DELETE ON friend_suggestions TO core_app;
GRANT UPDATE (profiling_enabled, hide_from_suggestions, updated_at), DELETE ON social_settings TO core_app;

-- +goose Down
DROP FUNCTION friend_suggestion_allowed(uuid, uuid);
DROP TABLE social_settings;
DROP TABLE friend_suggestions;
DROP TABLE friend_scores;
