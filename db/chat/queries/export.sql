-- KVKK veri dışa aktarma (issue #144, ADR-0021; alanlar
-- docs/data-export-format.md). Yalnızca @user_id'nin (doğrulanmış çağıran)
-- kendi kayıtları, @after'dan büyük anahtarla, anahtar sırasıyla. Mesaj
-- metni uygulamada çözülür ve hiçbir yere loglanmaz.

-- name: ExportChannelMessages :many
SELECT m.id, m.channel_id, c.tayfa_id, m.body_encrypted, m.key_version, m.created_at, m.deleted_at, m.kind, m.photo_upload_id
FROM channel_messages m JOIN channels c ON c.id = m.channel_id
WHERE m.author_id = @user_id AND m.id > @after
ORDER BY m.id
LIMIT @n;

-- Bir tayfanın iki tercihi (tayfa ve tayfa_now, issue #265) aynı scope_id'yi
-- taşır: sayfa scope_id'ye göre ilerler ve bir scope_id'nin bütün satırları
-- aynı sayfada gelir (sayfada en fazla @n farklı scope_id).
-- name: ExportNotificationPreferences :many
WITH page AS (
  SELECT DISTINCT x.scope_id FROM notification_preferences x
  WHERE x.user_id = @user_id AND x.scope_id > @after
  ORDER BY x.scope_id
  LIMIT @n
)
SELECT p.scope_id AS k, jsonb_build_object(
  'scope_type', p.scope_type, 'scope_id', p.scope_id, 'level', p.level,
  'muted_until', p.muted_until, 'updated_at', p.updated_at
) AS data
FROM notification_preferences p
JOIN page ON page.scope_id = p.scope_id
WHERE p.user_id = @user_id
ORDER BY p.scope_id, p.scope_type;
