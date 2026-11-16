package report

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// albumPhoto adds an album photo of uploader (an attendee) to a new event
// of the crew's tayfa, as the event service does.
func (e *env) albumPhoto(t *testing.T, c crew, uploader id.ID) id.ID {
	t.Helper()
	ev, photo := id.New(), id.New()
	_, err := e.pool.Exec(ctx, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
		VALUES ($1, $2, 'general', 'online', 'Göl', now(), now() + interval '1 hour', 'https://example.test/g', $3)`, ev.PG(), c.tayfa.PG(), c.admin.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
		VALUES ($1, $2, 'going', 'attended', 'online', now())`, ev.PG(), uploader.PG())
	must(t, err)
	up := mediatest.Ready(t, e.pool, uploader, media.PurposeEventPhoto, c.tayfa)
	_, err = e.pool.Exec(ctx, `UPDATE uploads SET attached_type = 'event_photo', attached_id = $2, attached_at = now() WHERE id = $1`, up.PG(), photo.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, client_request_id) VALUES ($1, $2, $3, $4, $5)`,
		photo.PG(), ev.PG(), uploader.PG(), up.PG(), id.New().PG())
	must(t, err)
	return photo
}

// An album photo is reportable content (issue #206): its uploader is the
// subject, it stays in the tayfa's queue and content_removed needs the
// photo gone.
func TestEventPhotoReport(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	photo := e.albumPhoto(t, c, c.member2)

	v := e.report(t, c, c.member, TargetEventPhoto, photo, ReasonPrivacy)
	q := e.queue(t, c, c.mod, StatusOpen)
	if len(q) != 1 || q[0].ID != v.ID || q[0].SubjectID != c.member2 || q[0].TargetType != TargetEventPhoto {
		t.Fatalf("kuyruk: %+v", q)
	}
	_, err := e.svc.Resolve(ctx, c.mod, c.tayfa, v.ID, ResolutionContentRemoved)
	is(t, "görünür fotoğraf", err, apperr.ErrReportActionMissing)

	_, err = e.pool.Exec(ctx, `UPDATE event_photos SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, photo.PG(), c.mod.PG())
	must(t, err)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, v.ID, ResolutionContentRemoved)
	must(t, err)
	_, _, err = e.svc.Create(ctx, c.mod2, c.tayfa, CreateInput{TargetType: TargetEventPhoto, TargetID: photo.String(), Reason: ReasonSpam})
	is(t, "silinmiş fotoğraf", err, apperr.ErrReportTargetNotFound)

	// Another tayfa's photo does not exist here (IDOR).
	other := e.crew(t)
	foreign := e.albumPhoto(t, other, other.member)
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetEventPhoto, TargetID: foreign.String(), Reason: ReasonSpam})
	is(t, "başka tayfanın fotoğrafı", err, apperr.ErrReportTargetNotFound)
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetEventPhoto, TargetID: id.New().String(), Reason: ReasonSpam})
	is(t, "bilinmeyen fotoğraf", err, apperr.ErrReportTargetNotFound)
}
