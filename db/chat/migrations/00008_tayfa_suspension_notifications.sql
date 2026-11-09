-- Platform askısı bildirimleri (issue #104 2. aşama): platform bir tayfayı
-- askıya alınca ve askıyı kaldırınca tayfanın yöneticileri ve
-- moderatörleri bildirim alır. Gerekçe bildirimde değil, tayfa
-- ayrıntısındadır.
--
-- Geri alma planı: Down bu türdeki bildirimleri siler ve kısıtı 00006'daki
-- haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended'
));

-- +goose Down
DELETE FROM notifications WHERE type IN ('tayfa_suspended', 'tayfa_unsuspended');
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived'
));
