-- Saklama işleri (issue #75; v14 "Saklama süreleri"). Her sorgu en fazla
-- @batch satır siler; iş, silinecek satır kalmayana kadar tekrar çağırır.
-- Koşullar silmede yeniden değerlendirilir, birden fazla pod aynı satırı
-- iki kez silmez (ikinci DELETE etkisizdir).

-- Kullanılmış ya da oturumu bitmiş (süresi dolmuş veya iptal edilmiş)
-- refresh token'lar 30 gün sonra.
-- name: PurgeRefreshTokens :execrows
DELETE FROM refresh_tokens
WHERE id IN (
  SELECT rt.id FROM refresh_tokens rt
  JOIN sessions s ON s.id = rt.session_id
  WHERE rt.used_at < @cutoff OR s.expires_at < @cutoff OR s.revoked_at < @cutoff
  LIMIT @batch
);

-- 90 gündür kullanılmayan ya da 90 gün önce kapatılmış oturumlar
-- (refresh token'ları CASCADE ile).
-- name: PurgeSessions :execrows
DELETE FROM sessions
WHERE id IN (
  SELECT x.id FROM sessions x
  WHERE x.last_refreshed_at < @cutoff OR x.revoked_at < @cutoff
  LIMIT @batch
);

-- 90 gündür görünmeyen cihazlar (oturumları CASCADE ile).
-- name: PurgeDevices :execrows
DELETE FROM devices
WHERE id IN (
  SELECT x.id FROM devices x WHERE x.last_seen_at < @cutoff LIMIT @batch
);

-- 30 gün önce silinmiş hesaplar kalıcı silinir. consent_records'un
-- foreign key'i yoktur ve dokunulmaz (00002, hukukçu kararı bekleniyor).
-- name: PurgeDeletedUsers :execrows
DELETE FROM users
WHERE id IN (
  SELECT x.id FROM users x WHERE x.deleted_at < @cutoff LIMIT @batch
);

-- Platform denetim kaydı 1 yıl (ADR-0008). Trigger 365 günden yeni satırın
-- silinmesini reddeder; koşul burada da veritabanı saatiyle denetlenir.
-- name: PurgePlatformAuditLogs :execrows
DELETE FROM platform_audit_logs
WHERE id IN (
  SELECT x.id FROM platform_audit_logs x
  WHERE x.created_at < @cutoff AND x.created_at < now() - interval '365 days'
  LIMIT @batch
);
