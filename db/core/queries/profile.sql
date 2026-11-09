-- Profil görünümü (issue #71, veri modeli v14 "Profil görünürlüğü",
-- "Kalıcı katılım geçmişi").

-- İkisinin de aktif üyesi olduğu canlı tayfalar. Yalnızca izleyicinin de
-- üyesi olduğu tayfalar döner; gizli bir tayfanın varlığı profilden sızmaz.
-- name: ListMutualTayfas :many
SELECT t.id, t.name
FROM tayfas t
JOIN memberships v ON v.tayfa_id = t.id AND v.user_id = @viewer_id AND v.status = 'active'
JOIN memberships s ON s.tayfa_id = t.id AND s.user_id = @subject_id AND s.status = 'active'
WHERE t.deleted_at IS NULL
ORDER BY t.name_normalized, t.id
LIMIT @max_count;

-- name: HasMutualTayfa :one
SELECT EXISTS (
  SELECT 1
  FROM memberships v
  JOIN memberships s ON s.tayfa_id = v.tayfa_id AND s.user_id = @subject_id AND s.status = 'active'
  JOIN tayfas t ON t.id = v.tayfa_id AND t.deleted_at IS NULL
  WHERE v.user_id = @viewer_id AND v.status = 'active'
);

-- Kişinin katılım geçmişi, yeniden eskiye; started_at @not_after'dan yeni
-- kayıtlar dönmez (14 günlük gecikme). Tayfa adı ve viewer_member yalnızca
-- izleyici o tayfanın aktif üyesiyse doludur; uygulama diğer alanları da
-- buna göre gizler. Sayfa imleci (started_at, id).
-- name: ListActivityPage :many
SELECT r.id, r.event_id, r.tayfa_id, r.event_type, r.mode, r.event_title,
       r.city, r.district, r.started_at, r.duration_minutes,
       (vm.user_id IS NOT NULL)::boolean AS viewer_member,
       COALESCE(t.name, '')::text AS tayfa_name
FROM user_activity_records r
LEFT JOIN memberships vm ON vm.tayfa_id = r.tayfa_id AND vm.user_id = @viewer_id AND vm.status = 'active'
LEFT JOIN tayfas t ON t.id = r.tayfa_id AND t.deleted_at IS NULL AND vm.user_id IS NOT NULL
WHERE r.user_id = @user_id
  AND r.started_at <= @not_after
  AND (r.started_at, r.id) < (@before_time::timestamptz, @before_id::uuid)
ORDER BY r.started_at DESC, r.id DESC
LIMIT @page_size;
