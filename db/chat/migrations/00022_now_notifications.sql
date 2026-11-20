-- Şimdi bildirimi (issue #265; ADR-0038 §4): bir şimdi etkinliği başladı.
-- core alıcıları belirler ve notification.requested ile ister: tayfa
-- etkinliğinde tayfanın etkin üyeleri (günde en fazla 3), anlık etkinlikte
-- organizatörün seçtiği arkadaşlar (davet; yayın başına en fazla 30). Aktör
-- organizatördür, hedef etkinliktir; başlık, tür, yer ya da ad taşınmaz.
--
--   notifications.type             now_started eklenir. Push gider
--       (enqueue_push değişmez); push'ta yalnızca push_now_started_title /
--       _body çeviri anahtarları ve kimlikler bulunur (ADR-0019).
--   notification_preferences       yeni kapsam tayfa_now: tayfa başına
--       "Şimdi bildirimleri" (yalnızca all / none). Tayfanın genel
--       seviyesi none ise şimdi bildirimi de push'lanmaz.
--
-- Geri alma planı: Down bu türdeki bildirimleri ve tayfa_now tercihlerini
-- siler (kullanıcıların "Şimdi bildirimleri" kapatması kaybolur), kısıtları
-- 00021'deki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned', 'now_started'
));

ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_scope_type;
ALTER TABLE notification_preferences ADD CONSTRAINT notification_preferences_scope_type
  CHECK (scope_type IN ('tayfa', 'tayfa_now', 'channel', 'dm_conversation'));
ALTER TABLE notification_preferences ADD CONSTRAINT notification_preferences_now_level
  CHECK (scope_type <> 'tayfa_now' OR level IN ('all', 'none'));

-- +goose Down
DELETE FROM notification_preferences WHERE scope_type = 'tayfa_now';
ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_now_level;
ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_scope_type;
ALTER TABLE notification_preferences ADD CONSTRAINT notification_preferences_scope_type
  CHECK (scope_type IN ('tayfa', 'channel', 'dm_conversation'));
DELETE FROM notifications WHERE type = 'now_started';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned'
));
