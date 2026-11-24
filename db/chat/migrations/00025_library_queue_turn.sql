-- Ortak kütüphanede "sıra sende" bildirimi (issue #332; ADR-0040 §1,
-- platform sahibinin 2026-11-23 kararı): kalemin önceki ödüncü bitince
-- sıradaki üyenin ödünç isteği kendiliğinden açılır (lending.Promote) ve
-- core ona bu bildirimi notification.requested ile ister.
--
--   library_queue_turn   sırası gelen üyeye: ödünç isteği açıldı, sahibinin
--                        onayını bekliyor. Hedef kalemdir (library_item,
--                        00024), aktörü yoktur; kalemin sahibiyle arasında
--                        iki yönden engel varsa hiç yazılmaz.
--
-- Push'lanır (enqueue_push değişmez); push'ta yalnızca
-- push_library_queue_turn_title / _body çeviri anahtarları ve kimlikler
-- bulunur (ADR-0019 madde 4). Eşyanın ya da kişilerin adı taşınmaz.
--
-- Geri alma planı: Down bu türdeki bildirimleri siler (push işleri CASCADE
-- ile gider) ve kısıtı 00024'teki haline döndürür.

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
  'library_loan_handed_over', 'library_queue_turn'
));

-- +goose Down
DELETE FROM notifications WHERE type = 'library_queue_turn';
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
