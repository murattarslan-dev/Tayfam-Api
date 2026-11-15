-- Arkadaş önerisi bildirimi (issue #208, ADR-0028): core'un
-- friend-suggestions işi notification.requested ile ister; alıcı yalnızca
-- öneriyi alan kişidir. Tayfası ve aktörü yoktur; hedef önerilen kişidir
-- (target_type user). Puan ve gerekçe taşımaz.
--
-- Yalnızca uygulama içidir: enqueue_push bu tür için push işi açmaz
-- (ADR-0028 §8). Diğer türler değişmez.
--
-- Geri alma planı: Down bu türdeki bildirimleri siler, kısıtı 00013'teki
-- haline ve enqueue_push'u 00011'deki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion'
));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Arkadaş önerisi yalnızca uygulama içidir (ADR-0028).
  IF NEW.type = 'friend_suggestion' THEN
    RETURN NULL;
  END IF;
  INSERT INTO push_queue (notification_id) VALUES (NEW.id)
  ON CONFLICT (notification_id) DO NOTHING;
  RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO push_queue (notification_id) VALUES (NEW.id)
  ON CONFLICT (notification_id) DO NOTHING;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
DELETE FROM notifications WHERE type = 'friend_suggestion';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready'
));
