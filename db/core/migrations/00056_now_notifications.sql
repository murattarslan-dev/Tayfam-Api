-- Şimdi başlat: şimdi bildirimi, sınırlar ve alıcı ayarı (issue #265;
-- ADR-0038 §4, §5; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri
-- (2026-11-18)" §D).
--
--   social_settings.now_from_friends  arkadaşlardan şimdi bildirimi
--       (alıcının ayarı): all (varsayılan) / close / off. close: yalnızca
--       alıcının arkadaş listesinin ilk 20'sindeki arkadaşlardan (arkadaş
--       listesiyle aynı sıra, ADR-0030 §12). Alıcının profillemesi
--       kapalıysa (puan yok) close, all gibi davranır.
--   now_notification_counters  günlük (İstanbul günü) şimdi bildirimi
--       sayaçları: tayfa başına (scope tayfa, en fazla 3) ve kişi başına
--       arkadaş yayını (scope user, en fazla 2). Yalnızca sayı tutulur;
--       kime gittiği tutulmaz. 2 gün sonra retention işiyle, kişinin
--       satırları hesap silinince hemen silinir. Sayaç etkinlik
--       oluşturmayla aynı transaction'da artar.
--   now_close_friend(alıcı, gönderen)  gönderen, alıcının arkadaş
--       listesinin ilk 20'sinde mi. Sıra arkadaş listesinin sırasıdır
--       (ListFriendshipsPage): arkadaşlık puanı azalan, eşitlikte kimlik;
--       puan yalnızca iki tarafın da profillemesi açıksa sayılır; silinmiş
--       ya da platform banlı hesap listede yoktur. Puanın kendisi dışarı
--       çıkmaz.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down fonksiyonu, sayaç tablosunu ve ayar kolonunu
-- kaldırır. Kullanıcıların "Şimdi bildirimi kimden" ayarı kaybolur (hepsi
-- varsayılana döner); sayaçlar zaten en fazla 2 günlüktür. Uygulama kodu
-- da birlikte geri alınmalıdır.

-- +goose Up

ALTER TABLE social_settings
  ADD COLUMN now_from_friends text NOT NULL DEFAULT 'all',
  ADD CONSTRAINT social_settings_now_from_friends CHECK (now_from_friends IN ('all', 'close', 'off'));

CREATE TABLE now_notification_counters (
  scope      text NOT NULL,
  subject_id uuid NOT NULL,
  day        date NOT NULL,
  sent       integer NOT NULL,
  PRIMARY KEY (scope, subject_id, day),
  CONSTRAINT now_notification_counters_scope CHECK (scope IN ('tayfa', 'user')),
  CONSTRAINT now_notification_counters_sent CHECK (
    sent >= 1 AND sent <= CASE scope WHEN 'tayfa' THEN 3 ELSE 2 END
  )
);
CREATE INDEX now_notification_counters_day_idx ON now_notification_counters (day);

-- +goose StatementBegin
CREATE FUNCTION now_close_friend(recipient uuid, sender uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1 FROM (
      SELECT p.user_id AS other
      FROM friendships f
      JOIN user_profiles p
        ON p.user_id = (CASE WHEN f.user_low_id = recipient THEN f.user_high_id ELSE f.user_low_id END)
       AND p.deleted_at IS NULL
       AND NOT EXISTS (SELECT 1 FROM user_platform_bans b WHERE b.user_id = p.user_id AND b.banned_at IS NOT NULL)
      LEFT JOIN friend_scores fs
        ON fs.user_low_id = f.user_low_id AND fs.user_high_id = f.user_high_id
       AND NOT EXISTS (SELECT 1 FROM social_settings s
                       WHERE s.user_id IN (f.user_low_id, f.user_high_id) AND NOT s.profiling_enabled)
      WHERE (f.user_low_id = recipient OR f.user_high_id = recipient) AND f.status = 'accepted'
      ORDER BY COALESCE(fs.score, 0) DESC, p.user_id
      LIMIT 20
    ) top
    WHERE top.other = sender
  )
$$;
-- +goose StatementEnd

-- core_app (ADR-0015): varsayılan yetki (00016) SELECT ve INSERT'tir.
-- Sayaç yalnızca artar; satır saklama ve hesap silmede silinir.
GRANT UPDATE (now_from_friends) ON social_settings TO core_app;
GRANT UPDATE (sent), DELETE ON now_notification_counters TO core_app;

-- +goose Down
DROP FUNCTION now_close_friend(uuid, uuid);
DROP TABLE now_notification_counters;
ALTER TABLE social_settings DROP CONSTRAINT social_settings_now_from_friends;
ALTER TABLE social_settings DROP COLUMN now_from_friends;
