-- Push bildirimleri (FCM; issue #134; ADR-0019, ADR-0005 "Bildirimler",
-- ADR-0009; veri modeli v14 "Bildirimler").
--
-- push_tokens: push token'ı oturuma bağlıdır (ADR-0019 madde 1, ADR-0018'deki
-- "cihaz = oturum" ile aynı). session_id, token'ı kaydeden erişim token'ının
-- sid'idir; oturum başına bir token vardır. Oturum bitince (session.revoked,
-- user.sanctioned, user.deleted) token silinir; 60 gün yenilenmeyen token
-- saklama işiyle silinir. Aynı FCM token'ı yalnızca bir oturumda durur: cihaz
-- başka hesapla giriş yapınca eski kayıt silinir (UNIQUE). user_id ve
-- session_id identity'nin kimlikleridir; veritabanları arası foreign key
-- yoktur (ADR-0002). Token log'a, olaya, metriğe ve hata mesajına girmez.
--
-- push_queue: yazılan her bildirim kaydı için kalıcı gönderim işi (ADR-0019
-- madde 2). İşi notifications üzerindeki trigger, kaydı yazan transaction'ın
-- içinde açar; böylece kayıt yazıldıysa iş de vardır, yeniden başlatma işi
-- kaybetmez. Birebir mesaj bildiriminin sayacı güncellenince (yeni mesaj)
-- bekleyen iş yoksa yenisi açılır. Kaydı yazan transaction push'u beklemez;
-- gönderimi chat'in push işçisi yapar. Bildirim silinince işi de gider
-- (ON DELETE CASCADE: 90 gün saklama, hesap silme, tayfa kalıcı silme).
-- session_ids: geçici hatadan sonra yalnızca bu oturumların token'larına
-- yeniden denenir (NULL: kullanıcının tüm token'ları).
--
-- Geri alma planı: tablolar ve trigger yenidir; Down onları kaldırır
-- (kayıtlı push token'ları ve bekleyen işler gider; istemciler açılışta
-- token'ı yeniden kaydeder, bildirim listesi etkilenmez).

-- +goose Up
CREATE TABLE push_tokens (
  session_id   uuid PRIMARY KEY,
  user_id      uuid NOT NULL,
  platform     text NOT NULL,
  token        text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  refreshed_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT push_tokens_platform CHECK (platform IN ('android', 'ios')),
  -- FCM kayıt token'ı: harf, rakam ve : _ - . karakterleri.
  CONSTRAINT push_tokens_token CHECK (octet_length(token) BETWEEN 1 AND 1024 AND token ~ '^[A-Za-z0-9:_.-]+$'),
  CONSTRAINT push_tokens_token_once UNIQUE (token)
);
CREATE INDEX push_tokens_user_id_idx ON push_tokens (user_id, refreshed_at DESC);
CREATE INDEX push_tokens_refreshed_at_idx ON push_tokens (refreshed_at);

CREATE TABLE push_queue (
  notification_id uuid PRIMARY KEY REFERENCES notifications (id) ON DELETE CASCADE,
  attempts        smallint NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  session_ids     uuid[],
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT push_queue_attempts CHECK (attempts BETWEEN 0 AND 10),
  CONSTRAINT push_queue_session_ids CHECK (session_ids IS NULL OR cardinality(session_ids) BETWEEN 1 AND 10)
);
CREATE INDEX push_queue_due_idx ON push_queue (next_attempt_at);
CREATE INDEX push_queue_created_at_idx ON push_queue (created_at);

-- Kayıtla aynı transaction'da gönderim işi. Fonksiyon çağıranın yetkisiyle
-- çalışır (SECURITY INVOKER): chat_app'in push_queue'ya INSERT yetkisi
-- vardır, başka bir şey gerekmez.
-- +goose StatementBegin
CREATE FUNCTION enqueue_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO push_queue (notification_id) VALUES (NEW.id)
  ON CONFLICT (notification_id) DO NOTHING;
  RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER notifications_enqueue_push AFTER INSERT ON notifications
  FOR EACH ROW EXECUTE FUNCTION enqueue_push();
-- Birebir mesaj bildirimi: okunmamış bildirimin sayacı yeni mesajla
-- güncellenir (issue #101); her yeni mesaj bir push'tur. Bekleyen iş varsa
-- yenisi açılmaz (birleşir).
CREATE TRIGGER notifications_enqueue_push_unread AFTER UPDATE OF unread_count ON notifications
  FOR EACH ROW WHEN (NEW.read_at IS NULL AND NEW.unread_count IS NOT NULL)
  EXECUTE FUNCTION enqueue_push();

-- chat_app (ADR-0015): varsayılan SELECT, INSERT'e ek olarak yalnızca
-- gerekenler. push_tokens: kaydı yenileme (platform, token, yenilenme
-- zamanı) ve silme; kullanıcı ve oturum değişemez. push_queue: işçinin
-- kiralama ve yeniden deneme kolonları ve tamamlanınca silme; işin
-- bildirimi değişemez.
GRANT DELETE ON push_tokens, push_queue TO chat_app;
GRANT UPDATE (platform, token, refreshed_at) ON push_tokens TO chat_app;
GRANT UPDATE (attempts, next_attempt_at, session_ids) ON push_queue TO chat_app;

-- +goose Down
DROP TRIGGER notifications_enqueue_push_unread ON notifications;
DROP TRIGGER notifications_enqueue_push ON notifications;
DROP FUNCTION enqueue_push();
DROP TABLE push_queue;
DROP TABLE push_tokens;
