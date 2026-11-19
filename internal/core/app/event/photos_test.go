package event

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// album is an event with an attended member, a member who only said going
// and a clock at the start.
type album struct {
	e         *env
	tf, admin id.ID
	ev        Event
	attendee  id.ID
	going     id.ID
	c         *clock
	svc       *Service
}

func newAlbum(t *testing.T) *album {
	t.Helper()
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	a := &album{e: e, tf: tf, admin: admin, ev: ev, c: &clock{t: ev.StartsAt}}
	a.attendee = e.going(t, tf, ev.ID, authz.Member)
	a.going = e.going(t, tf, ev.ID, authz.Member)
	a.svc = e.clocked(t, a.c, nil)
	_, err := a.svc.CheckIn(ctx, a.attendee, tf, ev.ID, izmir)
	must(t, err)
	return a
}

// muteLong mutes u well past the test clocks (the clocked service runs a
// day ahead).
func muteLong(t *testing.T, e *env, tf, u id.ID) {
	t.Helper()
	e.exec(t, `UPDATE memberships SET muted_until = now() + interval '30 days', muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG(), id.New().PG())
}

func (a *album) upload(t *testing.T, u id.ID) id.ID {
	t.Helper()
	return mediatest.Ready(t, a.e.pool, u, media.PurposeEventPhoto, a.tf)
}

func (a *album) add(t *testing.T, u id.ID) (Photo, error) {
	t.Helper()
	return a.svc.AddPhoto(ctx, u, a.tf, a.ev.ID, PhotoInput{UploadID: a.upload(t, u), ClientRequestID: id.New().String()})
}

// Only a verified attendee adds photos, inside the window, not on a
// cancelled event (Notion Faz 2 §2).
func TestAddPhotoRules(t *testing.T) {
	a := newAlbum(t)
	e := a.e

	_, err := a.add(t, a.going)
	wantErr(t, "check-in'siz katılımcı", err, apperr.ErrNotAttended)
	noRSVP := e.seed(t, a.tf, authz.Member)
	_, err = a.add(t, noRSVP)
	wantErr(t, "katılım yanıtı yok", err, apperr.ErrNotAttended)
	_, err = a.add(t, a.admin)
	wantErr(t, "yönetici de katılmadıysa", err, apperr.ErrNotAttended)
	// Not a member: refused before anything else (IDOR).
	stranger := id.New()
	_, err = a.svc.AddPhoto(ctx, stranger, a.tf, a.ev.ID, PhotoInput{UploadID: mediatest.Ready(t, e.pool, stranger, media.PurposeEventPhoto, a.tf), ClientRequestID: id.New().String()})
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)

	// The window: [start, end + 7 days).
	a.c.t = a.ev.StartsAt.Add(-time.Minute)
	_, err = a.add(t, a.attendee)
	wantErr(t, "başlamadan önce", err, apperr.ErrAlbumClosed)
	a.c.t = a.ev.EndsAt.Add(7 * 24 * time.Hour)
	_, err = a.add(t, a.attendee)
	wantErr(t, "bitişten 7 gün sonra", err, apperr.ErrAlbumClosed)
	a.c.t = a.ev.EndsAt.Add(7*24*time.Hour - time.Second)
	_, err = a.add(t, a.attendee)
	must(t, err)
	a.c.t = a.ev.StartsAt
	p, err := a.add(t, a.attendee)
	must(t, err)
	if p.UploaderID != a.attendee || p.EventID != a.ev.ID || p.Caption != "" {
		t.Fatalf("fotoğraf: %+v", p)
	}

	// A muted attendee cannot publish.
	muted := e.going(t, a.tf, a.ev.ID, authz.Member)
	_, err = a.svc.CheckIn(ctx, muted, a.tf, a.ev.ID, izmir)
	must(t, err)
	muteLong(t, e, a.tf, muted)
	_, err = a.add(t, muted)
	wantErr(t, "susturulmuş", err, apperr.ErrMuted)

	// A cancelled event takes no photos; its album is still read.
	_, err = a.svc.Cancel(ctx, a.admin, a.tf, a.ev.ID, "")
	must(t, err)
	_, err = a.add(t, a.attendee)
	wantErr(t, "iptal", err, apperr.ErrEventCancelled)
	list, _, err := a.svc.ListPhotos(ctx, a.going, a.tf, a.ev.ID, 0, "")
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("iptal edilmiş etkinliğin albümü: %d", len(list))
	}
}

// The upload must be the caller's own READY event_photo upload of the
// tayfa; an album photo never becomes a cover.
func TestAddPhotoUpload(t *testing.T) {
	a := newAlbum(t)
	e := a.e
	add := func(upload id.ID, caption string) (Photo, error) {
		return a.svc.AddPhoto(ctx, a.attendee, a.tf, a.ev.ID, PhotoInput{UploadID: upload, Caption: caption, ClientRequestID: id.New().String()})
	}
	_, err := add(a.upload(t, a.going), "")
	wantErr(t, "başkasının yüklemesi", err, apperr.ErrUploadNotFound)
	_, err = add(mediatest.Ready(t, e.pool, a.attendee, media.PurposeCover, a.tf), "")
	wantErr(t, "kapak albüm fotoğrafı olamaz", err, apperr.ErrUploadNotFound)
	_, err = add(mediatest.Ready(t, e.pool, a.attendee, media.PurposeLogPhoto, a.tf), "")
	wantErr(t, "av fotoğrafı albüm fotoğrafı olamaz", err, apperr.ErrUploadNotFound)
	otherTf, _ := e.newTayfa(t)
	e.seedAs(t, otherTf, a.attendee, authz.Member)
	_, err = add(mediatest.Ready(t, e.pool, a.attendee, media.PurposeEventPhoto, otherTf), "")
	wantErr(t, "başka tayfanın yüklemesi", err, apperr.ErrUploadNotFound)
	_, err = add(id.ID{}, "")
	wantField(t, "yüklemesiz", err, "upload_id")
	_, err = add(a.upload(t, a.attendee), strings.Repeat("ş", 201))
	wantField(t, "uzun açıklama", err, "caption")
	_, err = add(a.upload(t, a.attendee), "satır\x00")
	wantField(t, "denetim karakteri", err, "caption")

	upload := a.upload(t, a.attendee)
	p, err := add(upload, "  Zirvede  ")
	must(t, err)
	if p.Caption != "Zirvede" || p.UploadID != upload {
		t.Fatalf("fotoğraf: %+v", p)
	}
	_, err = add(upload, "")
	wantErr(t, "aynı dosya iki kez", err, apperr.ErrUploadInUse)
	// Not a cover, of this or any event.
	in := input()
	in.CoverUploadID = upload
	_, err = e.svc.Create(ctx, a.admin, a.tf, in)
	wantErr(t, "albüm fotoğrafı kapak olamaz", err, apperr.ErrUploadNotFound)
	_, err = e.pool.Exec(ctx, "UPDATE events SET cover_upload_id = $1 WHERE id = $2", upload.PG(), a.ev.ID.PG())
	if err == nil {
		t.Fatal("veritabanı albüm fotoğrafını kapak yaptı")
	}

	// A repeat returns the first photo.
	crid := id.New().String()
	in2 := PhotoInput{UploadID: a.upload(t, a.attendee), Caption: "Göl", ClientRequestID: crid}
	first, err := a.svc.AddPhoto(ctx, a.attendee, a.tf, a.ev.ID, in2)
	must(t, err)
	again, err := a.svc.AddPhoto(ctx, a.attendee, a.tf, a.ev.ID, in2)
	must(t, err)
	if again.ID != first.ID || again.Caption != "Göl" || e.count(t, `SELECT count(*) FROM event_photos WHERE event_id = $1`, a.ev.ID.PG()) != 2 {
		t.Fatalf("tekrar gönderim: %+v %+v", first, again)
	}
}

// 30 per person and 500 per album; deleted photos do not count.
func TestPhotoLimits(t *testing.T) {
	a := newAlbum(t)
	e := a.e
	var last Photo
	for range PhotosPerUser {
		p, err := a.add(t, a.attendee)
		must(t, err)
		last = p
	}
	_, err := a.add(t, a.attendee)
	wantErr(t, "kişi başı 30", err, apperr.ErrEventLimit)
	must(t, a.svc.RemovePhoto(ctx, a.attendee, a.tf, a.ev.ID, last.ID))
	_, err = a.add(t, a.attendee)
	must(t, err)

	// Fill the album to 500 directly (triggers off), then the app refuses.
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, client_request_id)
		SELECT gen_random_uuid(), $1, gen_random_uuid(), u.id, gen_random_uuid()
		FROM (SELECT gen_random_uuid() AS id FROM generate_series(1, $2)) u`, a.ev.ID.PG(), PhotosPerAlbum-PhotosPerUser)
	must(t, err)
	must(t, tx.Commit(ctx))
	other := e.going(t, a.tf, a.ev.ID, authz.Member)
	_, err = a.svc.CheckIn(ctx, other, a.tf, a.ev.ID, izmir)
	must(t, err)
	_, err = a.add(t, other)
	wantErr(t, "albüm başına 500", err, apperr.ErrEventLimit)
}

// Concurrent additions never pass the per-person limit (the event row is
// locked).
func TestPhotoLimitConcurrent(t *testing.T) {
	a := newAlbum(t)
	for range PhotosPerUser - 2 {
		_, err := a.add(t, a.attendee)
		must(t, err)
	}
	const n = 8
	uploads := make([]id.ID, n)
	for i := range uploads {
		uploads[i] = a.upload(t, a.attendee)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			_, errs[i] = a.svc.AddPhoto(ctx, a.attendee, a.tf, a.ev.ID, PhotoInput{UploadID: uploads[i], ClientRequestID: id.New().String()})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, apperr.ErrEventLimit):
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	if ok != 2 || a.e.count(t, `SELECT count(*) FROM event_photos WHERE uploader_id = $1 AND deleted_at IS NULL`, a.attendee.PG()) != PhotosPerUser {
		t.Fatalf("eşzamanlı eklemede %d başarılı", ok)
	}
}

// Every active member sees the album, also while muted; paging is stable;
// deleted photos are left out.
func TestListPhotos(t *testing.T) {
	a := newAlbum(t)
	e := a.e
	var added []Photo
	for i := range 5 {
		a.c.t = a.ev.StartsAt.Add(time.Duration(i) * time.Minute)
		p, err := a.add(t, a.attendee)
		must(t, err)
		added = append(added, p)
	}
	must(t, a.svc.RemovePhoto(ctx, a.attendee, a.tf, a.ev.ID, added[2].ID))
	viewer := e.seed(t, a.tf, authz.Member)
	muteLong(t, e, a.tf, viewer)
	var got []Photo
	token := ""
	for {
		page, next, err := a.svc.ListPhotos(ctx, viewer, a.tf, a.ev.ID, 2, token)
		must(t, err)
		got = append(got, page...)
		if next == "" {
			break
		}
		token = next
	}
	want := []id.ID{added[0].ID, added[1].ID, added[3].ID, added[4].ID}
	if len(got) != len(want) {
		t.Fatalf("albüm: %d fotoğraf", len(got))
	}
	for i := range want {
		if got[i].ID != want[i] || got[i].UploaderID != a.attendee || got[i].UploadID == (id.ID{}) {
			t.Fatalf("sıra: %d %+v", i, got[i])
		}
	}
	_, _, err := a.svc.ListPhotos(ctx, id.New(), a.tf, a.ev.ID, 0, "")
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	other := e.create(t, a.tf, a.admin, input())
	_, _, err = a.svc.ListPhotos(ctx, viewer, a.tf, other.ID, 0, token)
	wantField(t, "başka etkinliğin sayfası", err, "page_token")
}

// The uploader removes their own (also while muted), the organizer any
// photo of their own event, moderators and admins any photo; others none.
func TestRemovePhoto(t *testing.T) {
	a := newAlbum(t)
	e := a.e
	mod := e.seed(t, a.tf, authz.Moderator)
	member := e.seed(t, a.tf, authz.Member)

	p1, err := a.add(t, a.attendee)
	must(t, err)
	err = a.svc.RemovePhoto(ctx, member, a.tf, a.ev.ID, p1.ID)
	wantErr(t, "başka üye", err, apperr.ErrPermissionDenied)
	err = a.svc.RemovePhoto(ctx, a.attendee, a.tf, id.New(), p1.ID)
	wantErr(t, "yanlış etkinlik", err, apperr.ErrEventNotFound)

	// An organizer who is a plain member: their own event only.
	e.allowMembers(t, a.tf, a.admin)
	orgEv := e.create(t, a.tf, member, input())
	orgAtt := e.going(t, a.tf, orgEv.ID, authz.Member)
	a.c.t = orgEv.StartsAt
	_, err = a.svc.CheckIn(ctx, orgAtt, a.tf, orgEv.ID, izmir)
	must(t, err)
	op, err := a.svc.AddPhoto(ctx, orgAtt, a.tf, orgEv.ID, PhotoInput{UploadID: a.upload(t, orgAtt), ClientRequestID: id.New().String()})
	must(t, err)
	must(t, a.svc.RemovePhoto(ctx, member, a.tf, orgEv.ID, op.ID))
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'event.photo_removed' AND actor_id = $1`, member.PG()) != 1 {
		t.Fatal("organizatörün kaldırması denetim kaydına yazılmalı")
	}

	// The moderator removes anyone's, the admin too.
	must(t, a.svc.RemovePhoto(ctx, mod, a.tf, a.ev.ID, p1.ID))
	err = a.svc.RemovePhoto(ctx, mod, a.tf, a.ev.ID, p1.ID)
	wantErr(t, "ikinci kez", err, apperr.ErrEventItemNotFound)
	p2, err := a.add(t, a.attendee)
	must(t, err)
	must(t, a.svc.RemovePhoto(ctx, a.admin, a.tf, a.ev.ID, p2.ID))

	// One's own, while muted; the file stays until the purge.
	p3, err := a.add(t, a.attendee)
	must(t, err)
	muteLong(t, e, a.tf, a.attendee)
	must(t, a.svc.RemovePhoto(ctx, a.attendee, a.tf, a.ev.ID, p3.ID))
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'event.photo_removed' AND actor_id = $1`, a.attendee.PG()) != 0 {
		t.Fatal("kendi fotoğrafını silmek denetim kaydına yazılmaz")
	}
	if s := mediatest.Status(t, e.pool, p3.UploadID); s != "ready" {
		t.Fatalf("soft delete dosyayı bırakmamalı: %s", s)
	}
	// As the 30-day purge does (core_app, triggers on).
	_, err = e.pool.Exec(ctx, "DELETE FROM event_photos WHERE id = $1", p3.ID.PG())
	must(t, err)
	if s := mediatest.Status(t, e.pool, p3.UploadID); s != "deleting" {
		t.Fatalf("kalıcı silmede dosya silinmeli: %s", s)
	}

	// A muted organizer cannot remove others' photos.
	p4, err := a.svc.AddPhoto(ctx, orgAtt, a.tf, orgEv.ID, PhotoInput{UploadID: a.upload(t, orgAtt), ClientRequestID: id.New().String()})
	must(t, err)
	muteLong(t, e, a.tf, member)
	err = a.svc.RemovePhoto(ctx, member, a.tf, orgEv.ID, p4.ID)
	wantErr(t, "susturulmuş organizatör", err, apperr.ErrMuted)
}

// A refusal of the database's guard trigger becomes the same API error as
// the app check (not a 500), even when the app check was bypassed or raced.
func TestPhotoInsertErr(t *testing.T) {
	for code, want := range map[string]error{
		"TF090": apperr.ErrEventLimit,
		"TF091": apperr.ErrNotAttended,
		"TF092": apperr.ErrEventCancelled,
	} {
		err := photoInsertErr(fmt.Errorf("sarılı: %w", &pgconn.PgError{Code: code}))
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, %v bekleniyordu", code, err, want)
		}
	}
	other := photoInsertErr(&pgconn.PgError{Code: "23505"})
	for _, sentinel := range []error{apperr.ErrEventLimit, apperr.ErrNotAttended, apperr.ErrEventCancelled} {
		if errors.Is(other, sentinel) {
			t.Fatalf("başka bir veritabanı hatası %v oldu", sentinel)
		}
	}
}

// With the app's checks passed, a photo the trigger refuses (here: the
// attendance withdrawn in the database between the check and the insert,
// as a concurrent correction could) comes back as not-attended.
func TestPhotoGuardMapsThroughApp(t *testing.T) {
	a := newAlbum(t)
	upload := a.upload(t, a.attendee)
	a.e.exec(t, `CREATE FUNCTION test_withdraw() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN UPDATE event_rsvps SET attendance = 'unknown', attendance_method = NULL, checked_in_at = NULL WHERE event_id = NEW.event_id AND user_id = NEW.uploader_id; RETURN NEW; END $$`)
	a.e.exec(t, `CREATE TRIGGER a_test_withdraw BEFORE INSERT ON event_photos FOR EACH ROW EXECUTE FUNCTION test_withdraw()`)
	_, err := a.svc.AddPhoto(ctx, a.attendee, a.tf, a.ev.ID, PhotoInput{UploadID: upload, ClientRequestID: id.New().String()})
	wantErr(t, "trigger reddi", err, apperr.ErrNotAttended)
}
