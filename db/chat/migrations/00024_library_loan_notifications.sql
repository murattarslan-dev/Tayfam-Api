-- Ortak kütüphane ödünç adımı bildirimleri (issue #321; ADR-0040 §1,
-- platform sahibinin 2026-11-23 kararı): core alıcıyı belirler ve
-- notification.requested ile ister. Aktör adımı atan üyedir; alıcı yalnızca
-- öbür taraftır ve aktörle arasında iki yönden engel olan hiç bildirim
-- almaz. Hedef kalemdir (yeni hedef türü library_item, kimlik kalemin
-- kimliği); eşyanın ya da kişilerin adı taşınmaz.
--
--   library_loan_requested     kalemin sahibine: bir üye istedi, ödünç
--                              isteği onda.
--   library_queue_joined       kalemin sahibine: bir üye sıraya girdi.
--   library_loan_approved      isteyene: sahibi onayladı.
--   library_loan_declined      isteyene: sahibi reddetti.
--   library_loan_handed_over   isteyene: sahibi teslim etti.
--
-- Beşi de push'lanır (enqueue_push değişmez); push'ta yalnızca
-- push_<tür>_title / _body çeviri anahtarları ve kimlikler bulunur
-- (ADR-0019 madde 4). library_due ve library_loan_orphaned yalnızca
-- uygulama içi kalır (00023).
--
-- Geri alma planı: Down bu türlerdeki bildirimleri siler (push işleri
-- CASCADE ile gider) ve kısıtları 00023'teki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned', 'now_started', 'library_due', 'library_loan_orphaned',
  'library_loan_requested', 'library_queue_joined', 'library_loan_approved', 'library_loan_declined',
  'library_loan_handed_over'
));
ALTER TABLE notifications DROP CONSTRAINT notifications_target_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_target_type CHECK (target_type IN (
  'tayfa', 'membership', 'announcement', 'event', 'question', 'answer', 'report', 'user', 'dm_conversation',
  'data_export', 'library_item'
));

-- +goose Down
DELETE FROM notifications WHERE type IN (
  'library_loan_requested', 'library_queue_joined', 'library_loan_approved', 'library_loan_declined',
  'library_loan_handed_over'
);
ALTER TABLE notifications DROP CONSTRAINT notifications_target_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_target_type CHECK (target_type IN (
  'tayfa', 'membership', 'announcement', 'event', 'question', 'answer', 'report', 'user', 'dm_conversation',
  'data_export'
));
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned', 'now_started', 'library_due', 'library_loan_orphaned'
));
