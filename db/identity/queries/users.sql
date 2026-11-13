-- name: CreateUser :exec
INSERT INTO users (id, display_name, locale)
VALUES (@id, @display_name, @locale);

-- name: GetUser :one
SELECT id, display_name, avatar_upload_id, locale, birth_date_enc,
       birth_date_visibility, birth_date_audience, activity_visibility,
       ref_code, registered_at, age_blocked_at, created_at
FROM users
WHERE id = @id AND deleted_at IS NULL;

-- Kayıt bir kez tamamlanır; yaş nedeniyle reddedilmiş veya silinmiş hesapta
-- tamamlanamaz. Etkilenen satır sayısı 0 ise kayıt zaten yapılmış ya da
-- engellenmiştir.
-- name: CompleteRegistration :execrows
UPDATE users
SET birth_date_enc = @birth_date_enc, registered_at = now()
WHERE id = @id
  AND registered_at IS NULL
  AND age_blocked_at IS NULL
  AND deleted_at IS NULL;

-- name: BlockRegistrationForAge :execrows
UPDATE users
SET age_blocked_at = now()
WHERE id = @id
  AND registered_at IS NULL
  AND age_blocked_at IS NULL
  AND deleted_at IS NULL;

-- name: SetRefCode :execrows
UPDATE users SET ref_code = @ref_code, ref_code_rotated_at = now()
WHERE id = @id AND ref_code IS NULL;

-- name: SetDisplayName :exec
UPDATE users SET display_name = @display_name WHERE id = @id AND deleted_at IS NULL;

-- Yaş nedeniyle reddedilen hesapta e-posta tutulmaz (veri azaltma).
-- name: ClearIdentityEmails :exec
UPDATE auth_identities SET email = NULL WHERE user_id = @user_id;

-- Referans kodu (veri modeli v14 "Arkadaşlık ve Davet (v11)", issue #71).

-- Kodu yeniler; eski kod aynı anda geçersiz olur (eski link çalışmaz).
-- Yalnızca kodu olan, kayıtlı ve silinmemiş hesapta çalışır.
-- name: RotateRefCode :execrows
UPDATE users SET ref_code = @ref_code, ref_code_rotated_at = now()
WHERE id = @id AND ref_code IS NOT NULL AND registered_at IS NOT NULL AND deleted_at IS NULL;

-- Kodla bulunan kişinin yalnızca kimliği, adı ve avatarı döner; kayıtlı ve
-- silinmemiş hesaplar. Tayfaları, geçmişi ve arkadaşları buradan çıkmaz.
-- name: FindUserByRefCode :one
SELECT id, display_name, avatar_upload_id
FROM users
WHERE ref_code = @ref_code AND registered_at IS NOT NULL AND deleted_at IS NULL;

-- Arama sınırı için aramayı yapan kişinin satırını kilitler: aynı kişinin
-- eşzamanlı aramaları sırayla sayılır. Yalnızca kayıtlı ve silinmemiş hesap.
-- name: LockRegisteredUser :one
SELECT id FROM users
WHERE id = @id AND registered_at IS NOT NULL AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: CountRecentRefCodeLookups :one
SELECT count(*) FROM ref_code_lookups
WHERE user_id = @user_id AND looked_up_at > @since;

-- Sayılmayacak kadar eski denemeler aynı kişinin bir sonraki aramasında
-- silinir; tablo kendi kendini küçük tutar.
-- name: DeleteOldRefCodeLookups :exec
DELETE FROM ref_code_lookups WHERE user_id = @user_id AND looked_up_at <= @since;

-- name: RecordRefCodeLookup :exec
INSERT INTO ref_code_lookups (id, user_id, looked_up_at) VALUES (@id, @user_id, @looked_up_at);

-- Referans kayıtta bir kez yazılır (trigger users_referral_once da
-- korur). Referans veren kayıtlı ve silinmemiş olmalıdır.
-- name: SetReferrer :execrows
UPDATE users u SET referred_by_user_id = @referrer_id, referred_at = now()
WHERE u.id = @id AND u.referred_at IS NULL AND u.id <> @referrer_id
  AND EXISTS (SELECT 1 FROM users r WHERE r.id = @referrer_id AND r.registered_at IS NOT NULL AND r.deleted_at IS NULL);

-- Katılım geçmişinin kime görüneceği (v14 "Profil görünürlüğü").
-- Avatar (issue #135): core'un, çağıranın kendi token'ıyla doğruladığı
-- yükleme kimliği; NULL avatarı kaldırır.
-- name: SetUserAvatar :execrows
UPDATE users SET avatar_upload_id = sqlc.narg(avatar_upload_id)
WHERE id = @id AND registered_at IS NOT NULL AND deleted_at IS NULL;

-- name: SetActivityVisibility :execrows
UPDATE users SET activity_visibility = @activity_visibility
WHERE id = @id AND registered_at IS NOT NULL AND deleted_at IS NULL;

-- Hesap silme (issue #75; v14 "Hesap silme", "İlişkiler ve Silme
-- Davranışı"). Kilit, aynı hesabın eşzamanlı silme isteklerini sıraya koyar.
-- name: LockLiveUser :one
SELECT id, registered_at FROM users
WHERE id = @id AND deleted_at IS NULL
FOR UPDATE;

-- Kişisel veriler hemen temizlenir; satır 30 gün sonra saklama işiyle
-- kalıcı silinir. referred_at kalır (users_referral_once yeniden referans
-- yazılmasını engeller).
-- name: SoftDeleteUser :execrows
UPDATE users
SET deleted_at = now(),
    display_name = NULL,
    avatar_upload_id = NULL,
    birth_date_enc = NULL,
    ref_code = NULL,
    ref_code_rotated_at = NULL,
    referred_by_user_id = NULL
WHERE id = @id AND deleted_at IS NULL;

-- name: DeleteUserRefCodeLookups :exec
DELETE FROM ref_code_lookups WHERE user_id = @user_id;

-- Görünen ad değiştirme (issue #184): 24 saatte en fazla 3 kez. Eski ad
-- saklanmaz; yalnızca değişikliğin zamanı sayılır.
-- name: UpdateDisplayName :execrows
UPDATE users SET display_name = @display_name
WHERE id = @id AND registered_at IS NOT NULL AND deleted_at IS NULL;

-- name: CountRecentDisplayNameChanges :one
SELECT count(*) FROM display_name_changes
WHERE user_id = @user_id AND changed_at > @since;

-- name: DeleteOldDisplayNameChanges :exec
DELETE FROM display_name_changes WHERE user_id = @user_id AND changed_at <= @since;

-- name: RecordDisplayNameChange :exec
INSERT INTO display_name_changes (id, user_id, changed_at) VALUES (@id, @user_id, @changed_at);

-- name: DeleteUserDisplayNameChanges :exec
DELETE FROM display_name_changes WHERE user_id = @user_id;
