-- Rozet bildirimi (issue #258; ADR-0039 §4): core bir rozet verince
-- notification.requested ile ister. Aktörü yoktur (sistem); rozetin adı ya
-- da anahtarı taşınmaz, istemci rozetleri profilden ya da tayfadan okur.
--
--   * Kişisel rozet (şampiyonluk dahil): alıcı rozeti kazanan kişidir;
--     hedef kişinin kendisidir (target_type user), tayfası yoktur.
--   * Tayfa rozeti: alıcılar tayfanın yönetici ve moderatörleridir; hedef
--     tayfadır (target_type tayfa).
--
-- Yalnızca uygulama içidir: enqueue_push bu tür için de push işi açmaz
-- (arkadaş önerisi ve kutlama gibi, 00020).
--
-- Geri alma planı: Down bu türdeki bildirimleri siler, kısıtı ve
-- enqueue_push'u 00020'deki haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned'
));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Arkadaş önerisi (ADR-0030), kutlama (issue #255) ve rozet (ADR-0039
  -- §4) yalnızca uygulama içidir.
  IF NEW.type IN ('friend_suggestion', 'celebration', 'badge_earned') THEN
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
DELETE FROM notifications WHERE type = 'badge_earned';
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration'
));
