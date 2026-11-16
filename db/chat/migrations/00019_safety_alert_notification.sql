-- Güvenlik kişisi alarmı (issue #210, ADR-0033 §10): core'un safety-alerts
-- işi notification.requested ile ister; alıcı yalnızca katılımcının seçtiği
-- güvenlik kişisidir. Tayfası ve aktörü yoktur; hedef katılımcıdır
-- (target_type user). İçerik ve konum taşımaz.
--
-- Push gider ve susturulamaz (yürüyüş alarmı gibi; push paketi). Push
-- trigger'ı (enqueue_push, 00016) değişmez: yalnızca friend_suggestion'ı
-- atlar.
--
-- Numara: 00017 #214'te (sohbet ekleri); 00018 #209'a (anlık etkinlik)
-- bırakıldı. Mimari oturumunun dağıtımında chat numarası verilmediği için
-- seçildi; çakışırsa birleştirmede yeniden numaralanır.
--
-- Geri alma planı: Down bu türdeki bildirimleri siler ve kısıtı 00016'daki
-- haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert'
));

-- +goose Down
DELETE FROM notifications WHERE type = 'safety_alert';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion'
));
