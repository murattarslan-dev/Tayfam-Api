-- İşlemi yapan kullanıcıyı transaction'a bildirir; memberships trigger'ı rol
-- hiyerarşisini buna göre doğrular (ADR-0004). Yalnızca transaction içinde
-- anlamlıdır: değer transaction bitince silinir (set_config is_local = true).
-- name: SetActor :exec
SELECT set_config('app.actor_id', @actor_id::text, true);

-- Kullanıcısız arka plan işi. Trigger'ın tanıdığı tek değer
-- 'account_deletion'dır; başka bir değer işlemi hata ile durdurur.
-- name: SetSystemJob :exec
SELECT set_config('app.system_job', @job::text, true);
