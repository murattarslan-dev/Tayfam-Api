-- Veri dışa aktarma hazır bildirimi (issue #144, ADR-0021): identity'nin
-- user.data_export_ready olayıyla kullanıcının kendisine yazılır. Tayfası
-- ve aktörü yoktur; hedef identity'deki talebin kimliğidir.
--
-- Geri alma planı: Down bu türdeki bildirimleri siler ve kısıtları
-- 00008'deki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready'
));
ALTER TABLE notifications DROP CONSTRAINT notifications_target_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_target_type CHECK (target_type IN (
  'tayfa', 'membership', 'announcement', 'event', 'question', 'answer', 'report', 'user', 'dm_conversation',
  'data_export'
));

-- +goose Down
DELETE FROM notifications WHERE type = 'data_export_ready';
ALTER TABLE notifications DROP CONSTRAINT notifications_target_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_target_type CHECK (target_type IN (
  'tayfa', 'membership', 'announcement', 'event', 'question', 'answer', 'report', 'user', 'dm_conversation'
));
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended'
));
