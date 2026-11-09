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

-- Araç (plaka dahil) kişisel veridir ve kişiye aittir: silinir.
-- name: DeleteUserConvoyVehicles :exec
DELETE FROM event_convoy_vehicles WHERE user_id = @user_id;

-- Malzeme listede kalır; üstlenen ve ekleyen boşalır.
-- name: ClearCampItemUserFields :exec
UPDATE event_camp_items
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
