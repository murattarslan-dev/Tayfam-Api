-- Kutlama bildirimi (issue #255; ADR-0025 §4): organizatör bir kutlama
-- bileşeninde kazananı ilk kez ilan edince core notification.requested ile
-- ister; alıcılar tayfanın ilan eden dışındaki etkin üyeleridir. Hedef
-- etkinliktir (target_type event); kazananın adı ya da kimliği taşınmaz
-- (misafir kazananın verilecek adı yoktur).
--
-- Yalnızca uygulama içidir: enqueue_push bu tür için de push işi açmaz
-- (arkadaş önerisi gibi, 00016).
--
-- Geri alma planı: Down bu türdeki bildirimleri siler, kısıtı 00018'deki ve
-- enqueue_push'u 00016'daki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration'
));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Arkadaş önerisi (ADR-0030) ve kutlama (issue #255) yalnızca uygulama
  -- içidir.
  IF NEW.type IN ('friend_suggestion', 'celebration') THEN
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
  -- Arkadaş önerisi yalnızca uygulama içidir (ADR-0030).
  IF NEW.type = 'friend_suggestion' THEN
    RETURN NULL;
  END IF;
  INSERT INTO push_queue (notification_id) VALUES (NEW.id)
  ON CONFLICT (notification_id) DO NOTHING;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
DELETE FROM notifications WHERE type = 'celebration';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert'
));
