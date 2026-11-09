-- Olaylar internal/platform/outbox.Write ile yazılır (sıralama kilidi). Bu
-- dosyada yalnızca hesap silmede gereken temizlik var.

-- Silinen kullanıcının henüz silinmemiş profil olayları görünen adını
-- taşır; hesap silinince outbox'ta kalmazlar (veri azaltma, ADR-0011).
-- name: DeleteUserProfileEvents :exec
DELETE FROM domain_events
WHERE subject_type = 'user' AND subject_id = @user_id AND type = 'user.profile_updated';

-- Veri modeli v14: domain_events.actor_id hesap silinince NULL olur.
-- name: ClearEventActor :exec
UPDATE domain_events SET actor_id = NULL WHERE actor_id = @user_id;
