-- Kişisel istatistik görünürlüğü (issue #267; ADR-0026 §4, ADR-0039 §3).
--
--   users.stats_visibility  kişinin istatistiklerini ve kişisel rozetlerini
--       profilinde kimin göreceği: none (varsayılan; yalnızca kendisi) /
--       friends / friends_and_mutual. Değerler ve kitle mantığı
--       profile_details.about_visibility ile aynıdır. core'a
--       user.profile_updated ile kopyalanır (user_profiles.stats_visibility).
--       Dışa aktarmada profille gelir; hesap silinince none'a döner.
--
-- Veri taşınmaz ve silinmez: mevcut hesaplar varsayılan none ile başlar
-- (platform sahibi: kullanıcı isterse paylaşır).
--
-- Geri alma planı: Down kolonu kaldırır; uygulama koduyla birlikte geri
-- alınmalıdır.

-- +goose Up

ALTER TABLE users
  ADD COLUMN stats_visibility text NOT NULL DEFAULT 'none',
  ADD CONSTRAINT users_stats_visibility CHECK (stats_visibility IN ('none', 'friends', 'friends_and_mutual'));

-- +goose Down

ALTER TABLE users
  DROP CONSTRAINT users_stats_visibility,
  DROP COLUMN stats_visibility;
