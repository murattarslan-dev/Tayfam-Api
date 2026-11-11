-- Birebir konuşma tercihinde seviye yalnızca all ya da none (issue #138;
-- platform sahibi, 2026-11-11): DM bildirimleri kişisel olduğundan
-- mentions anlamsızdır. Uygulama da reddeder; bu kısıt ikinci katmandır
-- (CLAUDE.md "Girdi doğrulama hem Go'da hem veritabanında").
--
-- Geri alma planı: Down yalnızca kısıtı kaldırır. Up'tan önce kural dışı
-- satır olamaz (uç bu sürümle geldi); yine de varsa silinir, bu "all"
-- (tercih yok) ile aynı anlamdadır ve push davranışını değiştirmez
-- (mentions DM bildirimlerini zaten geçiriyordu).

-- +goose Up
DELETE FROM notification_preferences WHERE scope_type = 'dm_conversation' AND level = 'mentions';
ALTER TABLE notification_preferences ADD CONSTRAINT notification_preferences_dm_level
  CHECK (scope_type <> 'dm_conversation' OR level IN ('all', 'none'));

-- +goose Down
ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_dm_level;
