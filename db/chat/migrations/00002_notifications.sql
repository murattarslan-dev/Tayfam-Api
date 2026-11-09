-- Uygulama içi bildirimler ve bildirim tercihleri (veri modeli v14
-- "Bildirimler", "Senaryo Kuralları → Bildirimler"; ADR-0009; issue #64).
--
-- Bildirim yalnızca ne olduğunu ve neyle ilgili olduğunu kimliklerle tutar:
-- tür, tayfa, işlemi yapan, hedef (etkinlik, duyuru, cevap, üyelik, tayfa)
-- ve gerekirse ek bir hedef kimliği (cevabın sorusu). Başlık, gövde, yer,
-- isim gibi içerik metni yazılmaz; istemci içeriği core'dan çeker.
--
-- Bildirimleri ve alıcılarını core belirler (ADR-0009): core aynı
-- transaction'da notification.requested olayı yazar, chat her alıcı için bir
-- satır açar; source_event_id o core olayıdır. Aynı olay iki kez gelirse
-- kullanıcı başına tek bildirim kalır (UNIQUE). 90 gün sonra arka plan işi siler (v14 "Saklama süreleri").
-- Hesap silinince kullanıcının bildirimleri ve tercihleri silinir, başkasının
-- bildirimindeki actor_id NULL olur (ADR-0009). Tayfa kalıcı silinince
-- (tayfa.purged) tayfanın bildirimleri ve tercihleri silinir.

-- +goose Up
CREATE TABLE notifications (
  id              uuid PRIMARY KEY,
  user_id         uuid NOT NULL,
  tayfa_id        uuid,
  type            text NOT NULL,
  actor_id        uuid,
  target_type     text NOT NULL,
  target_id       uuid NOT NULL,
  -- Yalnızca ek kimlikler ve sınıflandırma (ör. {"question_id": …},
  -- {"reason": "inactive"}); içerik metni yok.
  payload         jsonb NOT NULL DEFAULT '{}',
  source_event_id uuid NOT NULL,
  read_at         timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT notifications_type CHECK (type IN (
    'event_created', 'event_updated', 'event_cancelled', 'rsvp_promoted', 'hike_overdue',
    'announcement', 'answer_received', 'mention', 'join_request', 'join_approved',
    'report_resolved', 'dm_request', 'friend_request', 'friend_accepted', 'friend_birthday',
    'tayfa_invitation', 'tayfa_deletion_requested', 'tayfa_warned', 'tayfa_archived'
  )),
  CONSTRAINT notifications_target_type CHECK (target_type IN (
    'tayfa', 'membership', 'announcement', 'event', 'question', 'answer', 'report', 'user', 'dm_conversation'
  )),
  CONSTRAINT notifications_payload CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 1024),
  CONSTRAINT notifications_once UNIQUE (source_event_id, user_id)
);
-- Liste (en yeni önce) ve okunmamış sayısı (v14).
CREATE INDEX notifications_user_id_idx ON notifications (user_id, id DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;
-- 90 günlük saklama işi ve hesap silmede aktör alanı.
CREATE INDEX notifications_created_at_idx ON notifications (created_at);
CREATE INDEX notifications_actor_id_idx ON notifications (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX notifications_tayfa_id_idx ON notifications (tayfa_id) WHERE tayfa_id IS NOT NULL;

-- Sessize alma (v14). Sessize alınmış bildirim listede yine görünür; tercih
-- push gönderiminde okunur (push ayrı iş). Varsayılan (all, süresiz değil)
-- satır tutulmaz.
CREATE TABLE notification_preferences (
  user_id     uuid NOT NULL,
  scope_type  text NOT NULL,
  scope_id    uuid NOT NULL,
  level       text NOT NULL,
  muted_until timestamptz,
  updated_at  timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (user_id, scope_type, scope_id),
  CONSTRAINT notification_preferences_scope_type CHECK (scope_type IN ('tayfa', 'channel', 'dm_conversation')),
  CONSTRAINT notification_preferences_level CHECK (level IN ('all', 'mentions', 'none'))
);
CREATE INDEX notification_preferences_scope_idx ON notification_preferences (scope_type, scope_id);
CREATE TRIGGER notification_preferences_updated_at BEFORE UPDATE ON notification_preferences FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE notification_preferences;
DROP TABLE notifications;
