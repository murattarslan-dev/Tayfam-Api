-- name: CreateDevice :exec
INSERT INTO devices (id, user_id, platform)
VALUES (@id, @user_id, @platform);

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = now() WHERE id = @id;

-- Hesap silmede cihazlar hemen silinir; oturumlar ve refresh token'lar
-- ON DELETE CASCADE ile gider (veri modeli v14 "İlişkiler ve Silme
-- Davranışı").
-- name: DeleteUserDevices :execrows
DELETE FROM devices WHERE user_id = @user_id;

-- App Attest (ADR-0022 §4): girişte onaylanan anahtarı yeni cihaza bağlar.
-- Anahtar başka bir cihazdaysa satır değişmez (0): aynı anahtar ikinci bir
-- cihaza bağlanmaz.
-- name: BindDeviceAppAttestKey :execrows
UPDATE devices
SET app_attest_key_id = @key_id, app_attest_public_key = @public_key, app_attest_counter = 0
WHERE id = @id AND platform = 'ios' AND app_attest_key_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM devices d WHERE d.app_attest_key_id = @key_id);

-- Yenilemede doğrulanan imzanın sayacını saklar; sayaç yalnızca artar.
-- Satır değişmezse (0) imza eski ya da başka bir yenilemede kullanılmıştır.
-- name: AdvanceDeviceAppAttestCounter :execrows
UPDATE devices
SET app_attest_counter = @counter
WHERE id = @id AND app_attest_key_id = @key_id AND app_attest_counter < @counter;
