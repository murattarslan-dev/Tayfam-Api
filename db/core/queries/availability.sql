-- "Boşum": boş vakit (issue #207, ADR-0028). day takvim günüdür; "bugün"
-- (@today) uygulamada Europe/Istanbul tarihine göre hesaplanır ve her
-- sorguya verilir. Görünürlük kuralı (arkadaşlık, engel) app katmanında,
-- aynı veritabanında okunur.

-- Günü ekler ya da bütünüyle değiştirir; id yalnızca yeni satırda kullanılır.
-- name: UpsertAvailability :one
INSERT INTO user_availability (id, user_id, day, slots, note)
VALUES (@id, @user_id, @day, @slots::text[], sqlc.narg(note))
ON CONFLICT (user_id, day) DO UPDATE SET slots = EXCLUDED.slots, note = EXCLUDED.note
RETURNING day, slots, note, updated_at;

-- name: DeleteAvailability :execrows
DELETE FROM user_availability WHERE user_id = @user_id AND day = @day;

-- Kişinin bugünden itibaren günleri (en fazla 31).
-- name: ListAvailability :many
SELECT day, slots, note, updated_at
FROM user_availability
WHERE user_id = @user_id AND day >= @today
ORDER BY day
LIMIT 31;

-- Saklama işi: geçmiş günler (bugün hariç) kalıcı silinir.
-- name: DeletePastAvailability :execrows
DELETE FROM user_availability WHERE day < @today;

-- Hesap silme (user.deleted).
-- name: DeleteUserAvailability :exec
DELETE FROM user_availability WHERE user_id = @user_id;
