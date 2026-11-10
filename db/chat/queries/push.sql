-- Push bildirimleri (issue #134, ADR-0019). Token oturuma bağlıdır; her
-- yazma çağıranın oturumu (token'ın sid'i) ve kullanıcısıyla sınırlıdır.
-- Token değeri hiçbir sorgunun hata mesajına girmez; sorgular yalnızca
-- kimliklerle filtreler (token eşleşmesi hariç: aynı token başka oturumda
-- kalmasın).

-- Kayıt ile oturum bitişi ve yaptırım sırayla işlenir: kayıt önce
-- kullanıcıyı, sonra oturumu kilitler; oturum bitişi oturumu, yaptırım
-- kullanıcıyı kilitler. Böylece "bitti mi?" denetimi ile yazma arasında
-- biten oturumun token'ı kalmaz.
-- name: LockPushKey :exec
SELECT pg_advisory_xact_lock(hashtextextended('push:' || sqlc.arg(key)::uuid::text, 0));

-- Aynı FCM token'ı başka bir oturumda (aynı cihazda başka hesap ya da eski
-- oturum) kayıtlıysa o kayıt silinir: cihaz yalnızca son oturumun
-- bildirimlerini alır.
-- name: DeletePushTokenElsewhere :exec
DELETE FROM push_tokens WHERE token = @token AND session_id <> @session_id;

-- Oturumun token'ı (oturum başına bir). Oturum başka kullanıcıya ait
-- olamaz; olursa satır değişmez.
-- name: UpsertPushToken :execrows
INSERT INTO push_tokens (session_id, user_id, platform, token, refreshed_at)
VALUES (@session_id, @user_id, @platform, @token, @now)
ON CONFLICT (session_id) DO UPDATE
SET platform = EXCLUDED.platform, token = EXCLUDED.token, refreshed_at = EXCLUDED.refreshed_at
WHERE push_tokens.user_id = EXCLUDED.user_id;

-- Kullanıcı başına en çok @keep token (en son yenilenenler) kalır.
-- name: TrimUserPushTokens :execrows
DELETE FROM push_tokens t
WHERE t.user_id = @user_id AND t.session_id NOT IN (
  SELECT k.session_id FROM push_tokens k WHERE k.user_id = @user_id
  ORDER BY k.refreshed_at DESC, k.session_id DESC LIMIT @keep
);

-- Çıkışta istemcinin isteği ve session.revoked.
-- name: DeletePushTokenBySession :execrows
DELETE FROM push_tokens WHERE session_id = @session_id;

-- Yaptırım ve hesap silme.
-- name: DeleteUserPushTokens :execrows
DELETE FROM push_tokens WHERE user_id = @user_id;

-- FCM token'ı geçersiz dedi: yalnızca oturum hâlâ aynı token'la kayıtlıysa
-- silinir (istemci bu arada yenilemiş olabilir).
-- name: DeleteInvalidPushToken :execrows
DELETE FROM push_tokens WHERE session_id = @session_id AND token = @token;

-- Gönderim için kullanıcının token'ları, en son yenilenen önce.
-- name: ListUserPushTokens :many
SELECT session_id, platform, token
FROM push_tokens
WHERE user_id = @user_id
ORDER BY refreshed_at DESC, session_id DESC
LIMIT @n;

-- name: CountUserPushTokens :one
SELECT count(*) FROM push_tokens WHERE user_id = @user_id;

-- 60 gün yenilenmeyen token'lar.
-- name: PurgeStalePushTokens :execrows
DELETE FROM push_tokens WHERE refreshed_at < @before;

-- Gönderim kuyruğu.

-- Zamanı gelen işleri kiralar: deneme sayısı artar, iş kira süresi
-- boyunca başka işçiye görünmez. İşçi düşerse iş kira bitince yeniden
-- görünür. Bildirimin alanları birlikte döner.
-- name: ClaimPushJobs :many
UPDATE push_queue q
SET attempts = q.attempts + 1, next_attempt_at = @lease_until
FROM notifications n
WHERE n.id = q.notification_id AND q.notification_id IN (
  SELECT d.notification_id FROM push_queue d
  WHERE d.next_attempt_at <= @now
  ORDER BY d.next_attempt_at
  LIMIT @n
  FOR UPDATE SKIP LOCKED
)
RETURNING q.notification_id, q.attempts, q.session_ids, q.created_at AS queued_at,
  n.user_id, n.tayfa_id, n.type, n.target_type, n.target_id, n.payload, n.read_at;

-- name: DeletePushJob :exec
DELETE FROM push_queue WHERE notification_id = @notification_id;

-- Geçici hatadan sonra: yalnızca başarısız oturumlara, geri çekilerek.
-- name: RetryPushJob :exec
UPDATE push_queue SET next_attempt_at = @next_attempt_at, session_ids = sqlc.narg(session_ids)::uuid[]
WHERE notification_id = @notification_id;

-- Push kapalıyken ya da uzun kesintiden sonra kalan eski işler.
-- name: PurgeOldPushJobs :execrows
DELETE FROM push_queue WHERE created_at < @before;

-- Gönderimden önce okunan tercih (sessize alma; v14).
-- name: GetNotificationPreference :one
SELECT level, muted_until
FROM notification_preferences
WHERE user_id = @user_id AND scope_type = @scope_type AND scope_id = @scope_id;
