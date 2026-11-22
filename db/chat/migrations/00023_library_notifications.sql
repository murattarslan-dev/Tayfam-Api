-- Ortak kütüphane bildirimleri (issue #270; ADR-0040 §1): core alıcıları
-- belirler ve notification.requested ile ister. Aktörü yoktur (sistem);
-- hedef ödüncün tayfasıdır (target_type tayfa). Eşyanın adı, kişilerin adı
-- ya da tarih taşınmaz; istemci ödünçleri kütüphaneden okur.
--
--   library_due             iade tarihi geçti: borç alana tek hatırlatma.
--   library_loan_orphaned   eşya teslim edilmişken taraflardan biri
--                           tayfadan ayrıldı: iki tarafa uyarı (ayrılan
--                           artık üye olmasa da); kayıt 30 gün sonra
--                           kapanır.
--
-- İkisi de yalnızca uygulama içidir: enqueue_push bu türler için push işi
-- açmaz (ADR-0040 §1 "push yok").
--
-- Geri alma planı: Down bu türlerdeki bildirimleri siler, kısıtı ve
-- enqueue_push'u 00022'deki (00021'deki işlev) haline döndürür.

-- +goose Up
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned', 'now_started', 'library_due', 'library_loan_orphaned'
));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Arkadaş önerisi (ADR-0030), kutlama (issue #255), rozet (ADR-0039 §4)
  -- ve ortak kütüphane (ADR-0040 §1) yalnızca uygulama içidir.
  IF NEW.type IN ('friend_suggestion', 'celebration', 'badge_earned', 'library_due', 'library_loan_orphaned') THEN
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
DELETE FROM notifications WHERE type IN ('library_due', 'library_loan_orphaned');
ALTER TABLE notifications DROP CONSTRAINT notifications_type;
ALTER TABLE notifications ADD CONSTRAINT notifications_type CHECK (type IN (
  'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
  'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
  'report_resolved', 'dm_request', 'dm_message', 'friend_request', 'friend_accepted', 'friend_birthday',
  'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived',
  'tayfa_suspended', 'tayfa_unsuspended', 'data_export_ready', 'friend_suggestion', 'safety_alert',
  'celebration', 'badge_earned', 'now_started'
));
