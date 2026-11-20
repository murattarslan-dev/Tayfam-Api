-- core arka plan işleri (issue #66; internal/core/app/jobs). Hepsi kullanıcısız
-- çalışır: app.actor_id ve app.system_job ayarlanmaz, denetim kaydı
-- CreateSystemAuditLog ile işlemi yapan olmadan yazılır.
--
-- Her parti tek bir transaction'dır ve önce işin advisory kilidini dener
-- (TryJobLock); kilidi alamayan pod o turu atlar. Satırlar ayrıca SKIP LOCKED
-- ile seçilir ve UPDATE/DELETE koşulu yeniden kontrol eder: kullanıcının aynı
-- anda yaptığı bir değişiklik (silme iptali, dönüş kaydı) önce commit olursa
-- iş o satıra dokunmaz.

-- İş başına bir kilit; transaction bitince bırakılır.
-- name: TryJobLock :one
SELECT pg_try_advisory_xact_lock(hashtextextended(@lock_key::text, 0));

-- ---------------------------------------------------------------------------
-- Pasif tayfa temizliği (v14 "Tayfa yönetimi", Proje Planı §7)
-- ---------------------------------------------------------------------------

-- Uyarıdan sonra aktivite olan tayfa yeniden aktif olur.
-- name: ReactivateWarnedTayfas :many
UPDATE tayfas SET status = 'active', warned_at = NULL
WHERE id IN (
    SELECT t.id FROM tayfas t
    WHERE t.status = 'warned' AND t.deleted_at IS NULL AND t.last_activity_at > t.warned_at
    ORDER BY t.id
    LIMIT @n
    FOR NO KEY UPDATE SKIP LOCKED
  )
  AND status = 'warned' AND deleted_at IS NULL AND last_activity_at > warned_at
RETURNING id;

-- Uyarıdan sonra @warned_before'a kadar aktivite olmayan tayfa arşivlenir
-- (salt okunur). Askıdaki ya da silme bekleyen tayfaya dokunulmaz.
-- name: ArchiveWarnedTayfas :many
UPDATE tayfas SET status = 'archived', archived_at = @now
WHERE id IN (
    SELECT t.id FROM tayfas t
    WHERE t.status = 'warned' AND t.deleted_at IS NULL
      AND t.warned_at <= @warned_before AND t.last_activity_at <= t.warned_at
      AND t.suspended_at IS NULL AND t.deletion_requested_at IS NULL
    ORDER BY t.id
    LIMIT @n
    FOR NO KEY UPDATE SKIP LOCKED
  )
  AND status = 'warned' AND deleted_at IS NULL
  AND warned_at <= @warned_before AND last_activity_at <= warned_at
  AND suspended_at IS NULL AND deletion_requested_at IS NULL
RETURNING id;

-- Uyarı iki sebeple verilir:
--   inactive:    last_activity_at @inactive_before'dan eski (30 gün).
--   few_members: açılışından (dönüşen anlık tayfada dönüşümünden)
--                @created_before'a kadar (7 gün) geçmiş ve
--                @min_members'tan az aktif üyesi olan tayfa. Bu kural tayfa
--                başına bir kez uygulanır: daha önce few_members uyarısı
--                almış (audit_logs, yalnızca eklenir) tayfa bu sebeple tekrar
--                uyarılmaz; küçük ama aktif bir arkadaş grubu her hafta uyarı
--                almaz, hareketsiz kalırsa inactive kuralı yine işler.
-- Askıdaki ya da silme bekleyen tayfaya dokunulmaz. Anlık tayfa bu
-- kurallara girmez; kendi arşiv kuralı vardır (ArchiveInstantTayfas).
-- name: WarnTayfas :many
UPDATE tayfas t SET status = 'warned', warned_at = @now
WHERE t.id IN (
    SELECT c.id FROM tayfas c
    WHERE c.status = 'active' AND c.deleted_at IS NULL AND c.kind = 'standard'
      AND c.suspended_at IS NULL AND c.deletion_requested_at IS NULL
      AND (
        c.last_activity_at <= @inactive_before
        OR (
          COALESCE(c.converted_at, c.created_at) <= @created_before
          AND (SELECT count(*) FROM memberships m WHERE m.tayfa_id = c.id AND m.status = 'active') < @min_members::int
          AND NOT EXISTS (
            SELECT 1 FROM audit_logs a
            WHERE a.tayfa_id = c.id AND a.action = 'tayfa.warned' AND a.metadata ->> 'reason' = 'few_members')
        )
      )
    ORDER BY c.id
    LIMIT @n
    FOR NO KEY UPDATE SKIP LOCKED
  )
  AND t.status = 'active' AND t.deleted_at IS NULL AND t.kind = 'standard'
  AND t.suspended_at IS NULL AND t.deletion_requested_at IS NULL
RETURNING t.id, (t.last_activity_at <= @inactive_before)::boolean AS inactive;

-- Anlık tayfa, etkinliği bittikten (iptalde iptal anından) @ended_before'a
-- kadar (7 gün) geçince ya da canlı etkinliği kalmayınca arşivlenir
-- (ADR-0032 §6). Askıdaki ya da silme bekleyen tayfaya dokunulmaz.
-- name: ArchiveInstantTayfas :many
UPDATE tayfas t SET status = 'archived', archived_at = @now
WHERE t.id IN (
    SELECT c.id FROM tayfas c
    WHERE c.kind = 'instant' AND c.status <> 'archived' AND c.deleted_at IS NULL
      AND c.suspended_at IS NULL AND c.deletion_requested_at IS NULL
      AND NOT EXISTS (
        SELECT 1 FROM events e
        WHERE e.tayfa_id = c.id AND e.deleted_at IS NULL
          AND COALESCE(e.cancelled_at, e.ends_at) > @ended_before)
    ORDER BY c.id
    LIMIT @n
    FOR NO KEY UPDATE SKIP LOCKED
  )
  AND t.kind = 'instant' AND t.status <> 'archived' AND t.deleted_at IS NULL
  AND t.suspended_at IS NULL AND t.deletion_requested_at IS NULL
RETURNING t.id;

-- Bildirim alıcıları: aktif yöneticiler ve moderatörler, yönetici önce.
-- name: ListTayfaStaffIDs :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND role IN ('admin', 'moderator')
ORDER BY role_rank(role) DESC, user_id
LIMIT @n;

-- ---------------------------------------------------------------------------
-- 72 saatlik tayfa silme (v14 "Tayfa silmede bekleme")
-- ---------------------------------------------------------------------------

-- Bekleme süresi dolan tayfa soft delete edilir; silen sistemdir
-- (deleted_by boş). İptal edilen talepte deletion_scheduled_for boştur.
-- name: SoftDeleteDueTayfas :many
UPDATE tayfas SET deleted_at = @now
WHERE id IN (
    SELECT t.id FROM tayfas t
    WHERE t.deletion_scheduled_for <= @now AND t.deleted_at IS NULL
    ORDER BY t.deletion_scheduled_for, t.id
    LIMIT @n
    FOR NO KEY UPDATE SKIP LOCKED
  )
  AND deleted_at IS NULL AND deletion_scheduled_for <= @now
RETURNING id;

-- ---------------------------------------------------------------------------
-- 30 günlük kalıcı silme (v14 "Saklama süreleri", CLAUDE.md "Silme")
--
-- Tayfa satırı silinince bağlı her şey foreign key'lerle (ON DELETE CASCADE)
-- gider; memberships ve audit_logs trigger'ları tayfa satırı artık yokken
-- silmeye izin verir (00001). Katılım geçmişi (user_activity_records) SET
-- NULL ile kalır. İçerik tabloları parti parti silinir.
-- ---------------------------------------------------------------------------

-- name: ListPurgeableTayfas :many
SELECT id FROM tayfas
WHERE deleted_at <= @deleted_before
ORDER BY deleted_at, id
LIMIT @n
FOR UPDATE SKIP LOCKED;

-- name: PurgeTayfa :execrows
DELETE FROM tayfas WHERE id = @id AND deleted_at <= @deleted_before;

-- name: PurgeAnnouncements :execrows
DELETE FROM announcements WHERE id IN (
  SELECT x.id FROM announcements x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- Cevapları, oyları ve etiket bağları cascade ile gider.
-- name: PurgeQuestions :execrows
DELETE FROM questions WHERE id IN (
  SELECT x.id FROM questions x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- name: PurgeAnswers :execrows
DELETE FROM answers WHERE id IN (
  SELECT x.id FROM answers x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- Seçenekleri cascade ile gider (oyları soft delete'te silinmiştir).
-- name: PurgePolls :execrows
DELETE FROM polls WHERE id IN (
  SELECT x.id FROM polls x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- Günlük kaydı (issue #243); fotoğrafı uploads_release_ref ile silme
-- kuyruğuna girer.
-- name: PurgeLogEntries :execrows
DELETE FROM event_log_entries WHERE id IN (
  SELECT x.id FROM event_log_entries x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- Albüm fotoğrafı; dosyası uploads_release_ref ile silme kuyruğuna girer.
-- name: PurgeEventPhotos :execrows
DELETE FROM event_photos WHERE id IN (
  SELECT x.id FROM event_photos x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- Etkinlik soft delete'i bugün yok; kolon olduğu için iş hazırdır. Katılım,
-- türe özel tablolar ve av kayıtları cascade ile, bağlı anket ve seri şablonu
-- SET NULL ile.
-- name: PurgeEvents :execrows
DELETE FROM events WHERE id IN (
  SELECT x.id FROM events x WHERE x.deleted_at <= @deleted_before
  ORDER BY x.deleted_at, x.id LIMIT @n FOR UPDATE SKIP LOCKED);

-- ---------------------------------------------------------------------------
-- Dönmeme alarmı (v14 "Yürüyüşte beklenen dönüş"; `roll_call` bileşeni,
-- issue #242)
-- ---------------------------------------------------------------------------

-- Yola çıkmış (satır var = started_at dolu), dönmemiş, alarmı gönderilmemiş
-- ve beklenen dönüş + tolerans süresi @now'dan önce dolmuş katılımcılar.
-- Alarm ve tolerans, etkinliğin tür sürümündeki bileşenin parametreleridir
-- (`alarm`, `return_grace_minutes`; tolerans yoksa 30 dakika); alarmsız
-- yoklamada make_interval NULL olur ve satır seçilmez. Etkinlik iptal ya da
-- silinmiş olsa da alarm gider: kişi hâlâ dağda olabilir.
-- name: LockOverdueHikers :many
SELECT c.event_id, c.component_key, c.user_id, e.tayfa_id, e.created_by
FROM event_roll_call_checkins c
JOIN event_roll_calls p ON p.event_id = c.event_id AND p.component_key = c.component_key
JOIN events e ON e.id = c.event_id
WHERE c.returned_at IS NULL AND c.alert_sent_at IS NULL
  AND p.expected_return_at + make_interval(mins => roll_call_alarm_minutes(event_component_definition(c.event_id, c.component_key))) <= @now
ORDER BY c.event_id, c.component_key, c.user_id
LIMIT @n
FOR UPDATE OF c SKIP LOCKED;

-- name: MarkHikeAlertSent :execrows
UPDATE event_roll_call_checkins SET alert_sent_at = @now
WHERE event_id = @event_id AND component_key = @component_key AND user_id = @user_id
  AND returned_at IS NULL AND alert_sent_at IS NULL;

-- ---------------------------------------------------------------------------
-- Plaka temizliği (v14 "Konvoy aracı", "Saklama süreleri"; `vehicles`
-- bileşeni, issue #240, ADR-0025 "Plaka kuralı")
-- ---------------------------------------------------------------------------

-- Bitişi @ended_before'dan önce olan etkinliklerin plakaları boşaltılır.
-- name: ClearOldPlates :execrows
UPDATE event_vehicles SET plate = NULL
WHERE id IN (
    SELECT v.id FROM event_vehicles v
    JOIN events e ON e.id = v.event_id
    WHERE v.plate IS NOT NULL AND e.ends_at <= @ended_before
    ORDER BY v.id
    LIMIT @n
    FOR UPDATE OF v SKIP LOCKED
  )
  AND plate IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Misafir takma adı temizliği (issue #252; ADR-0037 §6)
-- ---------------------------------------------------------------------------

-- Bitişi @ended_before'dan önce olan etkinliklerin misafir takma adları
-- silinir; misafir satırı kalır (skor ve kura yeri "Misafir" olur).
-- name: ClearOldGuestNicknames :execrows
UPDATE event_guest_players SET nickname = NULL
WHERE id IN (
    SELECT g.id FROM event_guest_players g
    JOIN events e ON e.id = g.event_id
    WHERE g.nickname IS NOT NULL AND e.ends_at <= @ended_before
    ORDER BY g.id
    LIMIT @n
    FOR UPDATE OF g SKIP LOCKED
  )
  AND nickname IS NOT NULL;

-- Alarmın alıcıları: organizatör (tayfada hâlâ aktif üyeyse) ve aktif
-- yöneticiler ve moderatörler; dönmeyen kişinin kendisi hariç. Organizatör
-- önce, sonra yönetici, sonra moderatör.
-- name: ListHikeAlertRecipients :many
SELECT user_id FROM memberships
WHERE tayfa_id = @tayfa_id AND status = 'active' AND user_id <> @hiker_id
  AND (role IN ('admin', 'moderator') OR user_id = sqlc.narg(organizer_id))
ORDER BY COALESCE(user_id = sqlc.narg(organizer_id), false) DESC, role_rank(role) DESC, user_id
LIMIT @n;

-- Bir turun sonucu (issue #110, 00015). Başka bir pod'un daha yeni bitişi
-- ezilmez. Başarısız tur son başarılı zamanı korur.
-- name: RecordJobRun :exec
INSERT INTO job_runs (job, finished_at, succeeded, changed, last_succeeded_at)
VALUES (@job, @finished_at, @succeeded, @changed,
        CASE WHEN @succeeded::boolean THEN @finished_at::timestamptz END)
ON CONFLICT (job) DO UPDATE
SET finished_at = EXCLUDED.finished_at,
    succeeded = EXCLUDED.succeeded,
    changed = EXCLUDED.changed,
    last_succeeded_at = COALESCE(EXCLUDED.last_succeeded_at, job_runs.last_succeeded_at)
WHERE job_runs.finished_at < EXCLUDED.finished_at;
