-- Keşfet vitrini istatistikleri ve kişisel istatistik görünürlüğünün
-- kopyası (issue #267; ADR-0026 §3–§5, ADR-0039 §3).
--
--   tayfas.stats_showcase         tayfa istatistiklerini ve rozetlerini
--       Keşfet vitrininde göster (varsayılan açık). Yalnızca yönetici
--       değiştirir (izin tablosunda TayfaStatsShowcaseUpdate); değişiklik
--       denetim kaydına yazılır. Vitrin yalnızca Keşfet akışındaki tayfa
--       kartında ve tayfanın donmuş etkinlik sayısı 3'ü bulduğunda görünür
--       (eşik kodda sabittir; ADR-0026 §3).
--   user_profiles.stats_visibility identity'deki users.stats_visibility'nin
--       kopyası (ADR-0009 kopya modeli; user.profile_updated ile gelir):
--       none (varsayılan) / friends / friends_and_mutual. Kişisel
--       istatistik ve kişisel rozetler başkasının profilinde yalnızca buna
--       göre görünür. Hesap silinince mezar taşında none'a döner.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down iki kolonu kaldırır; vitrin ve profil istatistiği
-- uygulama koduyla birlikte geri alınmalıdır.

-- +goose Up

ALTER TABLE tayfas
  ADD COLUMN stats_showcase boolean NOT NULL DEFAULT true;

ALTER TABLE user_profiles
  ADD COLUMN stats_visibility text NOT NULL DEFAULT 'none',
  ADD CONSTRAINT user_profiles_stats_visibility CHECK (stats_visibility IN ('none', 'friends', 'friends_and_mutual'));

-- +goose Down

ALTER TABLE user_profiles
  DROP CONSTRAINT user_profiles_stats_visibility,
  DROP COLUMN stats_visibility;

ALTER TABLE tayfas
  DROP COLUMN stats_showcase;
