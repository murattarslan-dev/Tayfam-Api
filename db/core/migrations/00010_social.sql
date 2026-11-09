-- Arkadaşlık, engelleme ve doğrudan tayfa daveti sınırları (issue #71;
-- veri modeli v14 "Arkadaşlık ve Davet (v11)", "Mesaj İsteği Modeli (v7)",
-- "Profil görünürlüğü"; ADR-0009: friendships ve user_blocks core'dadır).
--
-- friendships: iki kullanıcı arasında tek satır (user_low_id < user_high_id,
--   uuid sırası). pending → accepted. Reddedilen ya da geri çekilen istek
--   ve biten arkadaşlık satırla birlikte silinir; reddedilen isteği gönderen
--   bunu görmez.
-- user_blocks: tek yönlü kayıt, iki yönlü etki. Engelleme aynı
--   transaction'da iki kişi arasındaki arkadaşlığı ve bekleyen isteği siler;
--   engellenen kişi istek atamaz, profil görünmez (DM kuralları chat'te,
--   core'a sorarak).
-- user_profiles.activity_visibility: identity'nin users.activity_visibility
--   değerinin kopyası (user.profile_updated, ADR-0009). Profil görünümü
--   katılım geçmişini buna göre gösterir.
--
-- Günlük sınırlar (arkadaşlık isteği 20, doğrudan davet 10) veritabanından
-- sayılır (ADR-0004): her istek ve davet domain_events'e (actor_id = gönderen)
-- yazılır ve yalnızca eklenen bu kayıtlar sayılır. Reddedilip silinen istek
-- de sayılmaya devam eder; satır silmek sınırı sıfırlamaz.
--
-- Kullanıcı kimlikleri identity'dedir; veritabanları arası foreign key
-- yoktur (ADR-0002). Hesap silinince core bu satırları user.deleted
-- olayıyla siler.

-- +goose Up
CREATE TABLE friendships (
  user_low_id  uuid NOT NULL,
  user_high_id uuid NOT NULL,
  status       text NOT NULL,
  requested_by uuid NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  accepted_at  timestamptz,

  PRIMARY KEY (user_low_id, user_high_id),
  CONSTRAINT friendships_order CHECK (user_low_id < user_high_id),
  CONSTRAINT friendships_status CHECK (status IN ('pending', 'accepted')),
  CONSTRAINT friendships_requested_by CHECK (requested_by IN (user_low_id, user_high_id)),
  CONSTRAINT friendships_accepted_at CHECK ((status = 'accepted') = (accepted_at IS NOT NULL))
);
-- Kullanıcının arkadaşları ve istekleri her iki kolondan aranır.
CREATE INDEX friendships_user_high_idx ON friendships (user_high_id);

CREATE TABLE user_blocks (
  blocker_id uuid NOT NULL,
  blocked_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (blocker_id, blocked_id),
  CONSTRAINT user_blocks_not_self CHECK (blocker_id <> blocked_id)
);
-- İki yönlü engel kontrolü.
CREATE INDEX user_blocks_blocked_idx ON user_blocks (blocked_id);

ALTER TABLE user_profiles ADD COLUMN activity_visibility text NOT NULL DEFAULT 'tayfa';
ALTER TABLE user_profiles ADD CONSTRAINT user_profiles_activity_visibility
  CHECK (activity_visibility IN ('private', 'tayfa', 'public'));

-- Günlük istek ve davet sayımı: gönderenin son 24 saatteki olayları.
CREATE INDEX domain_events_actor_limit_idx ON domain_events (actor_id, type, occurred_at)
  WHERE type IN ('friend.requested', 'tayfa.invited') AND actor_id IS NOT NULL;

-- Hesap silmede kişinin gönderdiği bekleyen doğrudan davetler.
CREATE INDEX memberships_invited_by_idx ON memberships (invited_by_user_id) WHERE status = 'invited';

-- +goose Down
DROP INDEX memberships_invited_by_idx;
DROP INDEX domain_events_actor_limit_idx;
ALTER TABLE user_profiles DROP CONSTRAINT user_profiles_activity_visibility;
ALTER TABLE user_profiles DROP COLUMN activity_visibility;
DROP TABLE user_blocks;
DROP TABLE friendships;
