-- Hesap silme (identity'nin user.deleted olayı, ADR-0009). Hepsi
-- SetSystemJob('account_deletion') sonrası aynı transaction'da çalışır;
-- memberships ve audit_logs trigger'ları yalnızca bu sistem işine izin
-- verilen değişiklikleri kabul eder.

-- Tayfalar kilitlenme olmaması için tayfa_id sırasıyla işlenir.
-- name: ListMembershipsOfUser :many
SELECT tayfa_id, role, status
FROM memberships
WHERE user_id = @user_id
ORDER BY tayfa_id;

-- Silinmiş tayfalar dahil; tayfa satırını kilitler.
-- name: LockTayfaForSystem :one
SELECT id, deleted_at, deletion_requested_at
FROM tayfas
WHERE id = @id
FOR NO KEY UPDATE;

-- Son yöneticinin halefi (veri modeli v14 "Son yönetici koruması"): en
-- eski moderatör, yoksa en eski üye.
-- name: PickAdminSuccessor :one
SELECT user_id
FROM memberships
WHERE tayfa_id = @tayfa_id AND user_id <> @user_id
  AND status = 'active' AND role IN ('moderator', 'member')
ORDER BY role_rank(role) DESC, joined_at, user_id
LIMIT 1;

-- Kimse kalmayan tayfa sistem tarafından silme beklemesine alınır; talep
-- eden kişi yoktur.
-- name: ScheduleTayfaDeletionBySystem :execrows
UPDATE tayfas
SET deletion_requested_at = @requested_at, deletion_requested_by = NULL,
    deletion_scheduled_for = @scheduled_for
WHERE id = @id AND deleted_at IS NULL AND deletion_requested_at IS NULL;

-- name: ClearTayfaUserFields :exec
UPDATE tayfas
SET created_by            = NULLIF(created_by, @user_id),
    deletion_requested_by = NULLIF(deletion_requested_by, @user_id),
    suspended_by          = NULLIF(suspended_by, @user_id),
    deleted_by            = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (created_by, deletion_requested_by, suspended_by, deleted_by);

-- name: ClearInviteCreator :exec
UPDATE invites SET created_by = NULL WHERE created_by = @user_id;

-- name: ClearMembershipUserFields :exec
UPDATE memberships
SET invited_by_user_id = NULLIF(invited_by_user_id, @user_id),
    approved_by        = NULLIF(approved_by, @user_id),
    muted_by           = NULLIF(muted_by, @user_id)
WHERE @user_id IN (invited_by_user_id, approved_by, muted_by);

-- Duyurular kalır, yazar "Silinmiş kullanıcı" olur (v14 "İlişkiler ve
-- Silme Davranışı").
-- name: ClearAnnouncementUserFields :exec
UPDATE announcements
SET author_id  = NULLIF(author_id, @user_id),
    deleted_by = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (author_id, deleted_by);

-- Etkinlikler kalır, organizatör ve iptal eden "Silinmiş kullanıcı" olur;
-- seriler de öyle (v14 "İlişkiler ve Silme Davranışı": yazar kolonları
-- SET NULL).
-- name: ClearEventUserFields :exec
UPDATE events
SET created_by   = NULLIF(created_by, @user_id),
    cancelled_by = NULLIF(cancelled_by, @user_id),
    deleted_by   = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (created_by, cancelled_by, deleted_by);

-- name: ClearEventSeriesCreator :exec
UPDATE event_series SET created_by = NULL WHERE created_by = @user_id;

-- Başkasının yoklamasını işaretleyen kişi boşalır; kişinin kendi katılım
-- kayıtları silinir (kullanıcı → event_rsvps CASCADE).
-- name: ClearRsvpMarker :exec
UPDATE event_rsvps SET attendance_marked_by = NULL WHERE attendance_marked_by = @user_id;

-- name: DeleteUserRsvps :exec
DELETE FROM event_rsvps WHERE user_id = @user_id;

-- Araç (plaka dahil) kişisel veridir ve kişiye aittir: silinir (araca gelen
-- koltuk istekleri foreign key ile gider). Kişinin başkasının aracına
-- yazdığı koltuk isteği de silinir (`vehicles` bileşeni, issue #240).
-- name: DeleteUserVehicles :exec
DELETE FROM event_vehicles WHERE user_id = @user_id;

-- name: DeleteUserSeatRequests :exec
DELETE FROM event_vehicle_seat_requests WHERE user_id = @user_id;

-- Liste maddesi listede kalır; üstlenen ve ekleyen boşalır (`checklist`
-- bileşeni, issue #241).
-- name: ClearChecklistItemUserFields :exec
UPDATE event_checklist_items
SET assigned_user_id = NULLIF(assigned_user_id, @user_id),
    created_by       = NULLIF(created_by, @user_id)
WHERE @user_id IN (assigned_user_id, created_by);

-- name: ClearAuditActor :exec
UPDATE audit_logs SET actor_id = NULL WHERE actor_id = @user_id;

-- name: ClearEventActor :exec
UPDATE domain_events SET actor_id = NULL WHERE actor_id = @user_id;

-- Sistemin olayları txn.Event (internal/platform/outbox.Write) ile, işlemi
-- yapan olmadan yazılır.
-- name: CreateSystemAuditLog :exec
INSERT INTO audit_logs (id, tayfa_id, actor_id, action, target_type, target_id, metadata)
VALUES (@id, @tayfa_id, NULL, @action, @target_type, @target_id, @metadata);

-- Kişinin "geliyorum" dediği, bitmemiş ve iptal edilmemiş etkinlikler;
-- kişinin yeri her birinde bekleme listesine geçer. Etkinlik satırları
-- katılım akışıyla (SetRsvp) aynı kilitle, kimlik sırasıyla kilitlenir.
-- name: LockUserGoingUpcomingEvents :many
SELECT e.id, e.tayfa_id, e.capacity
FROM event_rsvps r
JOIN events e ON e.id = r.event_id
WHERE r.user_id = @user_id AND r.status = 'going'
  AND e.deleted_at IS NULL AND e.status = 'scheduled' AND e.ends_at > @now
ORDER BY e.id
FOR UPDATE OF e;

-- Kalıcı katılım geçmişi kişiye aittir: hesapla birlikte silinir (v14
-- "Kalıcı katılım geçmişi", user_id → CASCADE).
-- name: DeleteUserActivityRecords :exec
DELETE FROM user_activity_records WHERE user_id = @user_id;

-- Yoklama satırları (`roll_call` bileşeni, issue #242) kişiye aittir.
-- name: DeleteUserRollCallCheckins :exec
DELETE FROM event_roll_call_checkins WHERE user_id = @user_id;

-- Günlük kayıtları (av kayıtları dahil) konum içerebilir; kişiyle birlikte
-- silinir, fotoğrafları silme kuyruğuna girer (issue #243).
-- name: DeleteUserLogEntries :exec
DELETE FROM event_log_entries WHERE user_id = @user_id;

-- name: ClearLogEntryDeleter :exec
UPDATE event_log_entries SET deleted_by = NULL WHERE deleted_by = @user_id;

-- Kişisel ölçüm değerleri kişiye aittir; kişiyle birlikte silinir. Toplam
-- ölçüm etkinliğindir: kalır, yazan boşalır (issue #245; ADR-0025 §11).
-- name: DeleteUserMeasurements :exec
DELETE FROM event_measurements WHERE user_id = @user_id;

-- name: ClearMeasurementEnteredBy :exec
UPDATE event_measurements SET entered_by = NULL WHERE entered_by = @user_id;

-- Takımlardaki yeri kişinin katılımıdır; kişiyle birlikte silinir (issue
-- #248). Takımın kendisi etkinliğindir, kalır.
-- name: DeleteUserTeamMemberships :exec
DELETE FROM event_team_members WHERE user_id = @user_id;

-- Kuradaki yeri kişinin katılımıdır (issue #249). Kura sonucu sabittir:
-- yer silinmez, boşalır; sıra ve diğer yerler değişmez.
-- name: ClearUserDrawEntries :exec
UPDATE event_draw_entries SET user_id = NULL WHERE user_id = @user_id;

-- Yazbozdaki yeri ve skorları kalır, kişi boşalır: masada "Silinmiş
-- kullanıcı" olarak görünür (issue #250; ADR-0025 §11).
-- name: ClearUserScorePlayers :exec
UPDATE event_score_players SET user_id = NULL WHERE user_id = @user_id;

-- Eklediği misafirlerin takma adı onun içeriğidir; takma ad ve ekleyen
-- boşalır, misafir satırı kalır: skor ve kura yeri "Misafir" olarak görünür
-- (issue #252; ADR-0037 §6).
-- name: ClearGuestPlayerCreator :exec
UPDATE event_guest_players SET nickname = NULL, created_by = NULL WHERE created_by = @user_id;

-- Elle girilen derecesi kişiye aittir; kişiyle birlikte silinir. Başkasının
-- derecesini girdiyse derece kalır, yazan boşalır (issue #253).
-- name: DeleteUserRankingEntries :exec
DELETE FROM event_ranking_entries WHERE user_id = @user_id;

-- name: ClearRankingEntryEnteredBy :exec
UPDATE event_ranking_entries SET entered_by = NULL WHERE entered_by = @user_id;

-- Elle seçildiği kutlama kazananlığı kişiye aittir; kişiyle birlikte silinir.
-- İlan ettiği kutlama kalır, ilan eden boşalır (issue #255).
-- name: DeleteUserCelebrationWins :exec
DELETE FROM event_celebration_winners WHERE user_id = @user_id;

-- name: ClearCelebrationAnnouncer :exec
UPDATE event_celebrations SET announced_by = NULL WHERE announced_by = @user_id;

-- Etkinlik soruları ve oyları kişiye aittir (anonim olanlar dahil); kişiyle
-- birlikte silinir (sorunun oyları cascade ile). Cevaplandı işaretlediği ya
-- da sildiği başkasının sorusu kalır, işaretleyen ve silen boşalır (issue
-- #256; ADR-0025 §11).
-- name: DeleteUserEventQuestions :exec
DELETE FROM event_questions WHERE author_id = @user_id;

-- name: DeleteUserEventQuestionVotes :exec
DELETE FROM event_question_votes WHERE user_id = @user_id;

-- name: ClearEventQuestionUserFields :exec
UPDATE event_questions
SET answered_by = NULLIF(answered_by, @user_id), deleted_by = NULLIF(deleted_by, @user_id)
WHERE answered_by = @user_id OR deleted_by = @user_id;

-- Kişinin rozetleri kişiyle birlikte silinir (ADR-0039 §5). Tayfa
-- rozetleri tayfanındır, kalır.
-- name: DeleteUserBadges :exec
DELETE FROM user_badges WHERE user_id = @user_id;

-- Kişinin istatistiği kişiyle birlikte silinir (issue #266; ADR-0026).
-- Tayfa toplamları tayfanındır; bir sonraki hesaplamada kişinin silinen
-- kayıtları düşer.
-- name: DeleteUserStats :exec
DELETE FROM user_stats WHERE user_id = @user_id;

-- Oylama (issue #254): kişinin verdiği oylar ve kişiye, aracına ya da
-- fotoğrafına verilen oylar silinir (araçlar ve fotoğraflar silinmeden önce
-- çalışır). Yazdığı seçenekler ve kapattığı oylamalar etkinliğindir; kalır,
-- yazan ve kapatan boşalır.
-- name: DeleteUserEventVotes :exec
DELETE FROM event_votes AS ev
WHERE ev.user_id = @user_id::uuid OR ev.candidate_user_id = @user_id::uuid
  OR ev.event_vehicle_id IN (SELECT v.id FROM event_vehicles AS v WHERE v.user_id = @user_id::uuid)
  OR ev.photo_id IN (SELECT p.id FROM event_photos AS p WHERE p.uploader_id = @user_id::uuid);

-- name: ClearVoteOptionCreator :exec
UPDATE event_vote_options SET created_by = NULL WHERE created_by = @user_id;

-- name: ClearVoteCloser :exec
UPDATE event_vote_closures SET closed_by = NULL WHERE closed_by = @user_id;

-- Tayfa sözlüğüne eklediği değerler tayfanın ortak verisidir; kalır, ekleyen
-- boşalır (issue #244; ADR-0037 §4).
-- name: ClearVocabularyCreator :exec
UPDATE tayfa_vocabulary SET created_by = NULL WHERE created_by = @user_id;

-- Günün içeriği tayfanın havuzudur; içerik kalır, ekleyen ve silen boşalır
-- (issue #269; ADR-0027 §5, duyurular gibi).
-- name: ClearDailyItemUserFields :exec
UPDATE tayfa_daily_items
SET created_by = NULLIF(created_by, @user_id), deleted_by = NULLIF(deleted_by, @user_id)
WHERE created_by = @user_id OR deleted_by = @user_id;

-- Ortak kütüphane (issue #270; ADR-0040 §1): kalemler önce geri çekilir
-- (lending.Departed), sonra sahibi ve geri çekeni boşalır; ödünç
-- kayıtlarında borç alan boşalır; sıradaki yerler silinir.
-- name: ClearLibraryItemUserFields :exec
UPDATE library_items
SET owner_id = NULLIF(owner_id, @user_id), withdrawn_by = NULLIF(withdrawn_by, @user_id)
WHERE owner_id = @user_id OR withdrawn_by = @user_id;

-- name: ClearLibraryLoanBorrower :exec
UPDATE library_loans SET borrower_id = NULL WHERE borrower_id = @user_id;

-- name: DeleteUserLibraryQueue :exec
DELETE FROM library_queue WHERE user_id = @user_id;

-- Materyal kütüphanesi (issue #261; ADR-0040 §3): materyal kalır, yazar ve
-- silen boşalır; fotoğraf dosyaları media.UserDeleted ile silinir.
-- name: ClearMaterialUserFields :exec
UPDATE materials
SET author_id = NULLIF(author_id, @user_id), deleted_by = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (author_id, deleted_by);

-- Üye garajı (issue #272; ADR-0040 §4, KVKK): kişinin araçları ve
-- görünürlük ayarları silinir (fotoğraflar silme kuyruğuna girer;
-- etkinlik kayıtlarındaki garaj bağı boşalır); başkasının aracını kaldıran
-- olarak kişi boşalır.
-- name: DeleteUserGarageVehicles :exec
DELETE FROM garage_vehicles WHERE owner_id = @user_id;

-- name: ClearGarageVehicleDeleter :exec
UPDATE garage_vehicles SET deleted_by = NULL WHERE deleted_by = @user_id;

-- name: DeleteUserGarageVisibility :exec
DELETE FROM garage_tayfa_visibility WHERE user_id = @user_id;

-- Albüm fotoğrafları kişiseldir; kişiyle birlikte silinir, dosyaları
-- silme kuyruğuna girer (issue #206).
-- name: DeleteUserEventPhotos :exec
DELETE FROM event_photos WHERE uploader_id = @user_id;

-- name: ClearEventPhotoDeleter :exec
UPDATE event_photos SET deleted_by = NULL WHERE deleted_by = @user_id;

-- Sorular ve cevaplar kalır, yazar "Silinmiş kullanıcı" olur (v14
-- "İlişkiler ve Silme Davranışı").
-- name: ClearQuestionUserFields :exec
UPDATE questions
SET author_id  = NULLIF(author_id, @user_id),
    deleted_by = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (author_id, deleted_by);

-- name: ClearAnswerUserFields :exec
UPDATE answers
SET author_id  = NULLIF(author_id, @user_id),
    deleted_by = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (author_id, deleted_by);

-- Kişinin faydalı oyları silinir (kullanıcı → answer_votes CASCADE) ve
-- oy sayıları aynı transaction'da düşer. Bir kişinin bir cevapta en fazla
-- bir oyu olduğu için her cevap en fazla bir azalır.
-- name: DeleteUserAnswerVotes :exec
WITH gone AS (
  DELETE FROM answer_votes WHERE user_id = @user_id RETURNING answer_id
)
UPDATE answers a
SET vote_count = a.vote_count - 1
FROM gone
WHERE a.id = gone.answer_id;

-- Anketler kalır, açan ve silen "Silinmiş kullanıcı" olur; kişinin oyları
-- silinir (issue #60). Sayımlar buna göre düşer.
-- name: ClearPollUserFields :exec
UPDATE polls
SET created_by = NULLIF(created_by, @user_id),
    deleted_by = NULLIF(deleted_by, @user_id)
WHERE @user_id IN (created_by, deleted_by);

-- name: DeleteUserPollVotes :exec
DELETE FROM poll_votes WHERE user_id = @user_id;
