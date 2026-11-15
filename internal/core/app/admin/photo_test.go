package admin

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The panel shows a reported album photo (its caption and uploader) and
// removes it on a content_removed decision (issue #206).
func TestEventPhotoDecision(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ev, photo := id.New(), id.New()
	_, err := e.pool.Exec(ctx, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
		VALUES ($1, $2, 'general', 'online', 'Göl', now(), now() + interval '1 hour', 'https://example.test/g', $3)`, ev.PG(), c.tayfa.PG(), c.admin.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
		VALUES ($1, $2, 'going', 'attended', 'online', now())`, ev.PG(), c.member.PG())
	must(t, err)
	up := mediatest.Ready(t, e.pool, c.member, media.PurposeEventPhoto, c.tayfa)
	_, err = e.pool.Exec(ctx, `UPDATE uploads SET attached_type = 'event_photo', attached_id = $2, attached_at = now() WHERE id = $1`, up.PG(), photo.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, caption, client_request_id) VALUES ($1, $2, $3, $4, 'Göl kenarı', $5)`,
		photo.PG(), ev.PG(), c.member.PG(), up.PG(), id.New().PG())
	must(t, err)

	rep := e.escalate(t, c.tayfa.PG(), "event_photo", photo, c.member, c.mod, "privacy", "normal")
	_, item, err := e.svc.GetReportedItem(ctx, e.claims, rep)
	must(t, err)
	if item == nil || item.Title != "Göl kenarı" || item.Author != c.member {
		t.Fatalf("öğe = %+v", item)
	}
	must(t, e.svc.DecideReport(ctx, e.claims, rep, DecisionContentRemoved, reason))
	if e.count(t, `SELECT count(*) FROM event_photos WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2`, photo.PG(), e.admin.PG()) != 1 {
		t.Fatal("fotoğraf kaldırılmadı")
	}
}
