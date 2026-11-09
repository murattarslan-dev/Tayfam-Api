-- Kullanıcı profili kopyası (ADR-0009): core listelerde yazar adını ve
-- avatarını her istekte identity'ye sormadan gösterir. Kopya yalnızca
-- identity'nin user.profile_updated ve user.deleted olaylarıyla değişir
-- (UserEventService.ApplyUserEvent, ADR-0013); kısa süre eski kalması kabul
-- edilir.
--
-- ADR-0009'daki (user_id, display_name, avatar_upload_id, updated_at)
-- alanlarına iki alan eklenir:
--   source_event_id: son uygulanan identity olayı. Olaylar en az bir kez ve
--     sırası bozulabilir biçimde gelir; daha eski ya da aynı olay kopyayı
--     değiştirmez (idempotent tüketici, ADR-0002).
--   deleted_at: hesap silindi. Ad ve avatar temizlenir, satır mezar taşı
--     olarak kalır; geç gelen bir profil olayı silinen profili geri getiremez.

-- +goose Up
CREATE TABLE user_profiles (
  user_id          uuid PRIMARY KEY,
  display_name     text,
  avatar_upload_id uuid,
  source_event_id  uuid NOT NULL,
  deleted_at       timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_profiles_display_name_len CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 40),
  -- Silinmemiş profilin adı vardır; silinmiş profilde kişisel veri kalmaz.
  CONSTRAINT user_profiles_deleted CHECK (
    (deleted_at IS NULL AND display_name IS NOT NULL) OR
    (deleted_at IS NOT NULL AND display_name IS NULL AND avatar_upload_id IS NULL)
  )
);

-- Hesap silmede yazar ve aktör alanlarını boşaltmak için (davet eden,
-- onaylayan, susturan; davet linkini açan; tayfa alanları). memberships
-- (user_id) zaten indeksli.
CREATE INDEX invites_created_by_idx ON invites (created_by) WHERE created_by IS NOT NULL;

-- +goose Down
DROP INDEX invites_created_by_idx;
DROP TABLE user_profiles;
