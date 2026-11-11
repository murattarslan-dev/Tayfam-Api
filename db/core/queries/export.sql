-- KVKK veri dışa aktarma (issue #144, ADR-0021; alanlar
-- docs/data-export-format.md). Her sorgu yalnızca @user_id'nin (doğrulanmış
-- çağıran) kendi kayıtlarını, @after'dan büyük anahtarla, anahtar sırasıyla
-- en çok @n satır döner. Diğer kullanıcılar yalnızca kimlikle yer alır.
-- Kayıt JSON'u burada kurulur; uygulama yalnızca akıtır ve hiçbir alanı
-- log'a yazmaz.

-- name: ExportMemberships :many
SELECT m.tayfa_id AS k, jsonb_build_object(
  'tayfa_id', m.tayfa_id, 'tayfa_name', t.name, 'role', m.role, 'status', m.status,
  'joined_via', m.joined_via, 'joined_at', m.joined_at, 'muted_until', m.muted_until,
  'created_at', m.created_at
) AS data
FROM memberships m JOIN tayfas t ON t.id = m.tayfa_id
WHERE m.user_id = @user_id AND m.tayfa_id > @after
ORDER BY m.tayfa_id
LIMIT @n;

-- name: ExportEventRSVPs :many
SELECT r.event_id AS k, jsonb_build_object(
  'event_id', r.event_id, 'tayfa_id', e.tayfa_id, 'event_title', e.title, 'status', r.status,
  'attendance', r.attendance, 'attendance_method', r.attendance_method, 'checked_in_at', r.checked_in_at,
  'created_at', r.created_at, 'updated_at', r.updated_at
) AS data
FROM event_rsvps r JOIN events e ON e.id = r.event_id
WHERE r.user_id = @user_id AND r.event_id > @after
ORDER BY r.event_id
LIMIT @n;

-- name: ExportHikeCheckins :many
SELECT c.event_id AS k, jsonb_build_object(
  'event_id', c.event_id, 'started_at', c.started_at, 'returned_at', c.returned_at, 'created_at', c.created_at
) AS data
FROM event_hike_checkins c
WHERE c.user_id = @user_id AND c.event_id > @after
ORDER BY c.event_id
LIMIT @n;

-- name: ExportConvoyVehicles :many
SELECT v.id AS k, jsonb_build_object(
  'event_id', v.event_id, 'make', v.make, 'model', v.model, 'model_year', v.model_year,
  'color', v.color, 'plate', v.plate, 'created_at', v.created_at
) AS data
FROM event_convoy_vehicles v
WHERE v.user_id = @user_id AND v.id > @after
ORDER BY v.id
LIMIT @n;

-- name: ExportActivityRecords :many
SELECT a.id AS k, jsonb_build_object(
  'id', a.id, 'event_id', a.event_id, 'tayfa_id', a.tayfa_id, 'event_type', a.event_type, 'mode', a.mode,
  'event_title', a.event_title, 'city', a.city, 'district', a.district, 'started_at', a.started_at,
  'duration_minutes', a.duration_minutes, 'attendance_method', a.attendance_method
) AS data
FROM user_activity_records a
WHERE a.user_id = @user_id AND a.id > @after
ORDER BY a.id
LIMIT @n;

-- name: ExportAnnouncements :many
SELECT a.id AS k, jsonb_build_object(
  'id', a.id, 'tayfa_id', a.tayfa_id, 'title', a.title, 'body', a.body, 'pinned', a.pinned,
  'created_at', a.created_at, 'updated_at', a.updated_at, 'deleted_at', a.deleted_at
) AS data
FROM announcements a
WHERE a.author_id = @user_id AND a.id > @after
ORDER BY a.id
LIMIT @n;

-- name: ExportEvents :many
SELECT e.id AS k, jsonb_build_object(
  'id', e.id, 'tayfa_id', e.tayfa_id, 'type', e.type, 'mode', e.mode, 'title', e.title,
  'description', e.description, 'starts_at', e.starts_at, 'ends_at', e.ends_at,
  'meeting_point_name', e.meeting_point_name, 'meeting_lat', e.meeting_lat, 'meeting_lng', e.meeting_lng,
  'city', e.city, 'district', e.district, 'online_url', e.online_url, 'capacity', e.capacity,
  'status', e.status, 'cancelled_at', e.cancelled_at, 'cancel_reason', e.cancel_reason,
  'created_at', e.created_at, 'updated_at', e.updated_at, 'deleted_at', e.deleted_at
) AS data
FROM events e
WHERE e.created_by = @user_id AND e.id > @after
ORDER BY e.id
LIMIT @n;

-- name: ExportQuestions :many
SELECT q.id AS k, jsonb_build_object(
  'id', q.id, 'tayfa_id', q.tayfa_id, 'title', q.title, 'body', q.body,
  'created_at', q.created_at, 'updated_at', q.updated_at, 'deleted_at', q.deleted_at
) AS data
FROM questions q
WHERE q.author_id = @user_id AND q.id > @after
ORDER BY q.id
LIMIT @n;

-- name: ExportAnswers :many
SELECT a.id AS k, jsonb_build_object(
  'id', a.id, 'question_id', a.question_id, 'tayfa_id', a.tayfa_id, 'body', a.body, 'vote_count', a.vote_count,
  'created_at', a.created_at, 'updated_at', a.updated_at, 'deleted_at', a.deleted_at
) AS data
FROM answers a
WHERE a.author_id = @user_id AND a.id > @after
ORDER BY a.id
LIMIT @n;

-- name: ExportAnswerVotes :many
SELECT v.answer_id AS k, jsonb_build_object('answer_id', v.answer_id, 'created_at', v.created_at) AS data
FROM answer_votes v
WHERE v.user_id = @user_id AND v.answer_id > @after
ORDER BY v.answer_id
LIMIT @n;

-- name: ExportPolls :many
SELECT p.id AS k, jsonb_build_object(
  'id', p.id, 'tayfa_id', p.tayfa_id, 'event_id', p.event_id, 'question', p.question,
  'anonymous', p.anonymous, 'multiple_choice', p.multiple_choice,
  'options', (SELECT COALESCE(jsonb_agg(jsonb_build_object('id', o.id, 'text', o.text, 'position', o.position) ORDER BY o.position), '[]'::jsonb)
              FROM poll_options o WHERE o.poll_id = p.id),
  'closes_at', p.closes_at, 'closed_at', p.closed_at, 'created_at', p.created_at, 'deleted_at', p.deleted_at
) AS data
FROM polls p
WHERE p.created_by = @user_id AND p.id > @after
ORDER BY p.id
LIMIT @n;

-- Gizli anket dahil yalnızca kullanıcının kendi oyu (v14: gizli oyun
-- veritabanından çıktığı tek yer kullanıcının kendi dışa aktarmasıdır).
-- name: ExportPollVotes :many
SELECT v.option_id AS k, jsonb_build_object(
  'poll_id', v.poll_id, 'option_id', v.option_id, 'option_text', o.text, 'poll_question', p.question,
  'anonymous', p.anonymous, 'created_at', v.created_at
) AS data
FROM poll_votes v
JOIN poll_options o ON o.id = v.option_id
JOIN polls p ON p.id = v.poll_id
WHERE v.user_id = @user_id AND v.option_id > @after
ORDER BY v.option_id
LIMIT @n;

-- name: ExportFishingCatches :many
SELECT f.id AS k, jsonb_build_object(
  'id', f.id, 'event_id', f.event_id, 'species', f.species, 'length_cm', f.length_cm,
  'lat', f.lat, 'lng', f.lng, 'visibility', f.visibility, 'photo_upload_id', f.photo_upload_id,
  'created_at', f.created_at, 'deleted_at', f.deleted_at
) AS data
FROM fishing_catches f
WHERE f.user_id = @user_id AND f.id > @after
ORDER BY f.id
LIMIT @n;

-- name: ExportFriendships :many
SELECT (CASE WHEN f.user_low_id = @user_id::uuid THEN f.user_high_id ELSE f.user_low_id END)::uuid AS k, jsonb_build_object(
  'user_id', CASE WHEN f.user_low_id = @user_id::uuid THEN f.user_high_id ELSE f.user_low_id END,
  'status', f.status, 'requested_by_me', f.requested_by = @user_id::uuid,
  'created_at', f.created_at, 'accepted_at', f.accepted_at
) AS data
FROM friendships f
WHERE (f.user_low_id = @user_id::uuid OR f.user_high_id = @user_id::uuid)
  AND (CASE WHEN f.user_low_id = @user_id::uuid THEN f.user_high_id ELSE f.user_low_id END) > @after::uuid
ORDER BY 1
LIMIT @n;

-- name: ExportBlocks :many
SELECT b.blocked_id AS k, jsonb_build_object('user_id', b.blocked_id, 'created_at', b.created_at) AS data
FROM user_blocks b
WHERE b.blocker_id = @user_id AND b.blocked_id > @after
ORDER BY b.blocked_id
LIMIT @n;

-- Şikâyeti işleyen, öncelik ve platform alanları girmez (ADR-0021 madde 7).
-- name: ExportReports :many
SELECT r.id AS k, jsonb_build_object(
  'id', r.id, 'tayfa_id', r.tayfa_id, 'target_type', r.target_type, 'target_id', r.target_id,
  'reason', r.reason, 'description', r.description, 'evidence_text', r.evidence_text,
  'status', r.status, 'resolution', r.resolution, 'created_at', r.created_at
) AS data
FROM reports r
WHERE r.reporter_id = @user_id AND r.id > @after
ORDER BY r.id
LIMIT @n;

-- Yalnızca liste; dosyalar ve dışa aktarma kayıtları girmez.
-- name: ExportUploads :many
SELECT u.id AS k, jsonb_build_object(
  'id', u.id, 'purpose', u.purpose, 'status', u.status, 'tayfa_id', u.tayfa_id,
  'width', u.width, 'height', u.height, 'attached_type', u.attached_type, 'attached_id', u.attached_id,
  'created_at', u.created_at, 'ready_at', u.ready_at
) AS data
FROM uploads u
WHERE u.user_id = @user_id AND u.purpose <> 'data_export' AND u.status <> 'deleting' AND u.id > @after
ORDER BY u.id
LIMIT @n;
