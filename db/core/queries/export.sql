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

-- Yoklama satırları (`roll_call` bileşeni, issue #242). Bölümün adı Faz 1'den
-- kalmadır (hike_checkins); bütün türlerin yoklamalarını taşır. Bir
-- etkinlikte birden çok yoklama bileşeni olabilir: etkinlik başına bir satır.
-- name: ExportHikeCheckins :many
SELECT c.event_id AS k, jsonb_build_object(
  'event_id', c.event_id,
  'roll_calls', jsonb_agg(jsonb_build_object(
    'component_key', c.component_key, 'started_at', c.started_at,
    'returned_at', c.returned_at, 'created_at', c.created_at
  ) ORDER BY c.component_key)
) AS data
FROM event_roll_call_checkins c
WHERE c.user_id = @user_id AND c.event_id > @after
GROUP BY c.event_id
ORDER BY c.event_id
LIMIT @n;

-- Etkinliklere yazılan araçlar (`vehicles` bileşeni, issue #240). Bölümün adı
-- Faz 1'den kalmadır (convoy_vehicles); bütün türlerin araçlarını taşır.
-- name: ExportConvoyVehicles :many
SELECT v.id AS k, jsonb_build_object(
  'event_id', v.event_id, 'component_key', v.component_key, 'make', v.make, 'model', v.model,
  'model_year', v.model_year, 'color', v.color, 'plate', v.plate, 'seats', v.seats,
  'created_at', v.created_at, 'updated_at', v.updated_at
) AS data
FROM event_vehicles v
WHERE v.user_id = @user_id AND v.id > @after
ORDER BY v.id
LIMIT @n;

-- Kişinin başkasının aracına yazdığı koltuk istekleri. Aracın sahibi ve
-- plakası başkasının verisidir, dışa aktarılmaz.
-- name: ExportVehicleSeatRequests :many
SELECT r.id AS k, jsonb_build_object(
  'event_id', r.event_id, 'component_key', r.component_key, 'vehicle_id', r.event_vehicle_id,
  'status', r.status, 'created_at', r.created_at, 'decided_at', r.decided_at
) AS data
FROM event_vehicle_seat_requests r
WHERE r.user_id = @user_id AND r.id > @after
ORDER BY r.id
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

-- Günlük kayıtları (issue #243); bölümün adı Faz 1'den (fishing_catches).
-- Sözlüğe bağlı alanlar (issue #244) `vocabulary`'de: alan anahtarı →
-- seçilen ya da eklenen sözlük değeri (ADR-0037 §4).
-- name: ExportLogEntries :many
SELECT f.id AS k, jsonb_build_object(
  'id', f.id, 'event_id', f.event_id, 'component_key', f.component_key, 'values', f.field_values,
  'vocabulary', COALESCE((
    SELECT jsonb_object_agg(x.field_key, jsonb_build_object('vocabulary_id', v.id, 'value', v.value))
    FROM event_log_entry_vocabulary x JOIN tayfa_vocabulary v ON v.id = x.vocabulary_id
    WHERE x.entry_id = f.id), '{}'::jsonb),
  'lat', f.lat, 'lng', f.lng, 'location_visibility', f.location_visibility, 'photo_upload_id', f.photo_upload_id,
  'created_at', f.created_at, 'deleted_at', f.deleted_at
) AS data
FROM event_log_entries f
WHERE f.user_id = @user_id AND f.id > @after
ORDER BY f.id
LIMIT @n;

-- Kişisel ölçüm değerleri (`measure` bileşeni, issue #245): kendi
-- girdikleriniz ve organizatörün ya da hakemin sizin adınıza girdikleri. Kimin
-- girdiği yalnızca "kendim mi" olarak yer alır; toplam ölçümler etkinliğindir.
-- name: ExportMeasurements :many
SELECT m.id AS k, jsonb_build_object(
  'event_id', m.event_id, 'component_key', m.component_key, 'value', m.value,
  'entered_by_self', m.entered_by IS NOT DISTINCT FROM m.user_id,
  'created_at', m.created_at, 'updated_at', m.updated_at
) AS data
FROM event_measurements m
WHERE m.user_id = @user_id AND m.id > @after
ORDER BY m.id
LIMIT @n;

-- Takımlardaki yeriniz (`teams` bileşeni, issue #248): hangi etkinliğin
-- hangi takımındasınız. Takım arkadaşları ve takım adı (organizatörün
-- içeriği) dışa aktarılmaz.
-- name: ExportTeamMemberships :many
SELECT t.id AS k, jsonb_build_object(
  'event_id', m.event_id, 'component_key', m.component_key, 'team_id', m.team_id,
  'team_position', t.position, 'created_at', m.created_at
) AS data
FROM event_team_members m
JOIN event_teams t ON t.id = m.team_id
WHERE m.user_id = @user_id AND t.id > @after
ORDER BY t.id
LIMIT @n;

-- Kuradaki yeriniz (`draw` bileşeni, issue #249): hangi etkinliğin kurasında
-- kaçıncı sıradasınız. Kuradaki diğer kişiler dışa aktarılmaz.
-- name: ExportDrawEntries :many
SELECT d.id AS k, jsonb_build_object(
  'event_id', d.event_id, 'component_key', d.component_key, 'position', d.position,
  'drawn_at', w.drawn_at
) AS data
FROM event_draw_entries d
JOIN event_draws w ON w.event_id = d.event_id AND w.component_key = d.component_key
WHERE d.user_id = @user_id AND d.id > @after
ORDER BY d.id
LIMIT @n;

-- Yazbozdaki yerleriniz ve skorlarınız (`scoreboard` bileşeni, issue #250):
-- hangi etkinliğin hangi masasında oynadınız, el el skorunuz. Masadaki
-- diğer kişiler ve skorları dışa aktarılmaz.
-- name: ExportScores :many
SELECT p.id AS k, jsonb_build_object(
  'event_id', p.event_id, 'component_key', p.component_key, 'table_id', p.table_id,
  'table_label', t.label, 'preset_key', t.preset_key, 'position', p.position,
  'scores', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('round', s.round, 'points', s.points, 'outcome', s.outcome)
      ORDER BY s.round)
    FROM event_scores AS s WHERE s.player_id = p.id), '[]'::jsonb),
  'created_at', p.created_at
) AS data
FROM event_score_players AS p
JOIN event_score_tables AS t ON t.id = p.table_id
WHERE p.user_id = @user_id AND p.id > @after
ORDER BY p.id
LIMIT @n;

-- Eşleşme ağacındaki maçlarınız (`bracket` bileşeni, issue #251): kuradaki
-- yeriniz üzerinden hangi etkinliğin hangi turunda oynadınız ve oyun oyun
-- kendi skorunuz (`wdl` kuralında kendi sonucunuz). Rakip ve skoru dışa
-- aktarılmaz.
-- name: ExportBracketMatches :many
SELECT m.id AS k, jsonb_build_object(
  'event_id', m.event_id, 'component_key', m.component_key, 'round', m.round,
  'position', d.position,
  'games', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'number', g.number,
      'points', CASE WHEN m.a_position = d.position THEN g.a_points ELSE g.b_points END,
      'outcome', CASE WHEN m.a_position = d.position THEN g.a_outcome
        WHEN g.a_outcome = 'win' THEN 'loss' WHEN g.a_outcome = 'loss' THEN 'win'
        ELSE g.a_outcome END)
      ORDER BY g.number)
    FROM event_bracket_games AS g WHERE g.match_id = m.id), '[]'::jsonb),
  'created_at', m.created_at
) AS data
FROM event_bracket_matches AS m
JOIN event_draw_entries AS d ON d.event_id = m.event_id AND d.component_key = m.draw_key
  AND d.position IN (m.a_position, m.b_position)
WHERE d.user_id = @user_id AND m.id > @after
ORDER BY m.id
LIMIT @n;

-- Eklediğiniz misafir oyuncular (issue #252): hangi etkinliğe hangi takma
-- adla. Takma adı silinmiş misafir NULL takma adla gelir; misafirin masa ve
-- kura yerleri dışa aktarılmaz.
-- name: ExportGuestPlayers :many
SELECT g.id AS k, jsonb_build_object(
  'id', g.id, 'event_id', g.event_id, 'nickname', g.nickname, 'created_at', g.created_at
) AS data
FROM event_guest_players AS g
WHERE g.created_by = @user_id AND g.id > @after
ORDER BY g.id
LIMIT @n;

-- Elle girilen dereceleriniz (`ranking` bileşeni, issue #253): organizatörün
-- ya da hakemin sizin adınıza girdiği süre ya da sıra. Kimin girdiği yer
-- almaz; ölçümden hesaplanan dereceler ölçümlerinizdedir (measurements).
-- name: ExportRankingEntries :many
SELECT r.id AS k, jsonb_build_object(
  'event_id', r.event_id, 'component_key', r.component_key, 'value', r.value,
  'created_at', r.created_at, 'updated_at', r.updated_at
) AS data
FROM event_ranking_entries AS r
WHERE r.user_id = @user_id AND r.id > @after
ORDER BY r.id
LIMIT @n;

-- Kutlamalarda elle kazanan seçildiğiniz etkinlikler (`celebration`
-- bileşeni, issue #255). Kimin ilan ettiği ve diğer kazananlar yer almaz;
-- kaynaktan (yazboz, eşleşme ağacı, derece) gelen kazanmalar o bileşenlerin
-- bölümlerindedir.
-- name: ExportCelebrationWins :many
SELECT w.id AS k, jsonb_build_object(
  'event_id', w.event_id, 'component_key', w.component_key, 'announced_at', c.announced_at,
  'created_at', w.created_at
) AS data
FROM event_celebration_winners AS w
JOIN event_celebrations AS c ON c.event_id = w.event_id AND c.component_key = w.component_key
WHERE w.user_id = @user_id AND w.id > @after
ORDER BY w.id
LIMIT @n;

-- Etkinlik soruları (`questions` bileşeni, issue #256): sorduğunuz sorular
-- (anonim olanlar dahil; ADR-0025 §11) ve oy verdiğiniz sorular. Başkasının
-- sorusunun metni, yazarı ve diğer oylar yer almaz; kimin cevaplandı
-- işaretlediği de.
-- name: ExportEventQuestions :many
SELECT q.id AS k, jsonb_build_object(
  'id', q.id, 'event_id', q.event_id, 'component_key', q.component_key,
  'asked', q.author_id = @user_id,
  'body', CASE WHEN q.author_id = @user_id THEN q.body END,
  'anonymous', CASE WHEN q.author_id = @user_id THEN q.anonymous END,
  'answered_at', CASE WHEN q.author_id = @user_id THEN q.answered_at END,
  'created_at', CASE WHEN q.author_id = @user_id THEN q.created_at END,
  'deleted_at', CASE WHEN q.author_id = @user_id THEN q.deleted_at END,
  'voted_at', (SELECT v.created_at FROM event_question_votes v WHERE v.question_id = q.id AND v.user_id = @user_id)
) AS data
FROM event_questions AS q
WHERE (q.author_id = @user_id
       OR EXISTS (SELECT 1 FROM event_question_votes v WHERE v.question_id = q.id AND v.user_id = @user_id))
  AND q.id > @after
ORDER BY q.id
LIMIT @n;

-- Kazandığınız rozetler (issue #258; ADR-0039 §5): rozetin anahtarı,
-- dönemi ve kazanma zamanı; şampiyonlukta etkinlik ve tayfası. Rozetin
-- adı katalogdadır.
-- name: ExportUserBadges :many
SELECT b.id AS k, jsonb_build_object(
  'badge_key', b.badge_key, 'period', b.period_key, 'tayfa_id', b.tayfa_id, 'event_id', b.event_id,
  'earned_at', b.earned_at
) AS data
FROM user_badges AS b
WHERE b.user_id = @user_id AND b.id > @after
ORDER BY b.id
LIMIT @n;

-- Kişinin istatistiği (issue #266): tayfa, yıl (tüm zamanlarda null),
-- metrik, değer ve katkı veren etkinlik sayısı.
-- name: ExportUserStats :many
SELECT s.id AS k, jsonb_build_object(
  'tayfa_id', s.tayfa_id, 'year', NULLIF(s.year, 0), 'metric_key', s.metric_key, 'value', s.value,
  'event_count', s.event_count, 'computed_at', s.computed_at
) AS data
FROM user_stats AS s
WHERE s.user_id = @user_id AND s.id > @after
ORDER BY s.id
LIMIT @n;

-- Albüm fotoğrafları (issue #206); dosyaların kendisi zip'e girmez
-- (ADR-0021 §5), uploads bölümü onları listeler.
-- name: ExportEventPhotos :many
SELECT p.id AS k, jsonb_build_object(
  'id', p.id, 'event_id', p.event_id, 'tayfa_id', e.tayfa_id, 'upload_id', p.upload_id,
  'caption', p.caption, 'created_at', p.created_at, 'deleted_at', p.deleted_at
) AS data
FROM event_photos p
JOIN events e ON e.id = p.event_id
WHERE p.uploader_id = @user_id AND p.id > @after
ORDER BY p.id
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
  'evidence_upload_id', r.evidence_upload_id,
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

-- Tek kayıt: üç ayar (satır yoksa varsayılanlar) ve kişi hakkında puan
-- tutulan kişi sayısı. Puan değerleri ve kiminle olduğu girmez (ADR-0030
-- §9). Ayar satırı da puanı da yoksa tutulan bir şey yoktur, kayıt da yok.
-- name: ExportSocialSettings :many
SELECT @user_id::uuid AS k, jsonb_build_object(
  'profiling_enabled', COALESCE(s.profiling_enabled, true),
  'hide_from_suggestions', COALESCE(s.hide_from_suggestions, false),
  'discover_visible_to_friends', COALESCE(s.discover_visible_to_friends, true),
  'now_from_friends', COALESCE(s.now_from_friends, 'all'),
  'updated_at', s.updated_at,
  'scored_people_count', (SELECT count(*) FROM friend_scores f
                          WHERE f.user_low_id = @user_id::uuid OR f.user_high_id = @user_id::uuid)
) AS data
FROM (SELECT 1) one
LEFT JOIN social_settings s ON s.user_id = @user_id::uuid
WHERE @user_id::uuid > @after::uuid
  AND (s.user_id IS NOT NULL
       OR EXISTS (SELECT 1 FROM friend_scores f WHERE f.user_low_id = @user_id::uuid OR f.user_high_id = @user_id::uuid))
LIMIT @n;

-- Kişiye yapılan öneriler, kapatılanlar dahil. Engellemede yazılan kayıt
-- (source = blocked) bir öneri değildir; engel zaten blocks bölümündedir.
-- name: ExportFriendSuggestions :many
SELECT fs.suggested_user_id AS k, jsonb_build_object(
  'user_id', fs.suggested_user_id, 'created_at', fs.created_at, 'dismissed_at', fs.dismissed_at,
  'withdrawn_at', fs.withdrawn_at
) AS data
FROM friend_suggestions fs
WHERE fs.user_id = @user_id AND fs.source = 'suggested' AND fs.suggested_user_id > @after
ORDER BY fs.suggested_user_id
LIMIT @n;

-- "Boşum" (issue #207): kişinin kaydedilmiş boş günleri (geçmişi saklama
-- işi siler).
-- name: ExportAvailability :many
SELECT a.id AS k, jsonb_build_object(
  'date', a.day, 'slots', a.slots, 'note', a.note,
  'created_at', a.created_at, 'updated_at', a.updated_at
) AS data
FROM user_availability a
WHERE a.user_id = @user_id AND a.id > @after
ORDER BY a.id
LIMIT @n;
