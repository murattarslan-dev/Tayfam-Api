package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tests for 00023_event_photos.sql (issue #206, ADR-0028): the database's
// second line behind the app layer, as core_app sees it.

type albumFixture struct {
	pool     *pgxpool.Pool
	q        *Queries
	tayfa    id.ID
	event    id.ID
	attendee id.ID
}

func newAlbumFixture(t *testing.T) albumFixture {
	t.Helper()
	ctx := context.Background()
	pool, q := setup(t)
	tf := newTayfa(t, pool, id.New(), tayfaOpts{})
	ev, err := insertEvent(ctx, pool, tf, "general", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := albumFixture{pool: pool, q: q, tayfa: tf, event: ev, attendee: id.New()}
	f.rsvp(t, f.attendee, "attended")
	return f
}

func (f albumFixture) rsvp(t *testing.T, u id.ID, attendance string) {
	t.Helper()
	method := "NULL"
	if attendance == "attended" {
		method = "'location'"
	}
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
		 VALUES ($1, $2, 'going', $3, `+method+`, CASE WHEN $3 = 'attended' THEN now() END)`,
		f.event.PG(), u.PG(), attendance); err != nil {
		t.Fatalf("katılım: %v", err)
	}
}

// add attaches a new READY event_photo upload of u and writes the photo
// row in one transaction, as the app does.
func (f albumFixture) add(ctx context.Context, t *testing.T, u id.ID) error {
	t.Helper()
	upload := readyUpload(t, f.q, u, uploadOpts{purpose: "event_photo", tayfa: f.tayfa})
	return pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		return f.insert(ctx, New(tx), u, upload, true)
	})
}

func (f albumFixture) insert(ctx context.Context, q *Queries, u, upload id.ID, attach bool) error {
	pid := id.New()
	if attach {
		if _, err := q.AttachUpload(ctx, AttachUploadParams{
			ID: upload.PG(), UserID: u.PG(), Purpose: "event_photo", TayfaID: f.tayfa.PG(),
			AttachedType: "event_photo", AttachedID: pid.PG(), Now: ts(time.Now()),
		}); err != nil {
			return err
		}
	}
	return q.CreateEventPhoto(ctx, CreateEventPhotoParams{
		ID: pid.PG(), EventID: f.event.PG(), UploaderID: u.PG(), UploadID: upload.PG(),
		ClientRequestID: id.New().PG(), CreatedAt: ts(time.Now()),
	})
}

func TestEventPhotoGuard(t *testing.T) {
	ctx := context.Background()
	f := newAlbumFixture(t)

	// Only an attended uploader.
	going, noShow := id.New(), id.New()
	f.rsvp(t, going, "unknown")
	f.rsvp(t, noShow, "no_show")
	wantCode(t, f.add(ctx, t, going), CodePhotoNotAttended)
	wantCode(t, f.add(ctx, t, noShow), CodePhotoNotAttended)
	wantCode(t, f.add(ctx, t, id.New()), CodePhotoNotAttended)
	wantCode(t, f.add(ctx, t, f.attendee), "")

	// The file must be attached to the row (uploads_check_ref).
	upload := readyUpload(t, f.q, f.attendee, uploadOpts{purpose: "event_photo", tayfa: f.tayfa})
	wantCode(t, f.insert(ctx, f.q, f.attendee, upload, false), CodeMediaRef)
	// Another purpose cannot be attached as an album photo.
	cover := readyUpload(t, f.q, f.attendee, uploadOpts{purpose: "cover", tayfa: f.tayfa})
	if err := f.insert(ctx, f.q, f.attendee, cover, true); err == nil {
		t.Fatal("kapak albüm fotoğrafı oldu")
	}

	// No caption over 200 characters, and no empty caption.
	if _, err := f.pool.Exec(ctx, `UPDATE event_photos SET deleted_at = now()`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.Repeat("ş", 201)} {
		_, err := adminOf(t, f.pool).Exec(ctx, `UPDATE event_photos SET caption = $1`, bad)
		if constraintOf(err) != "event_photos_caption" {
			t.Fatalf("açıklama %d karakter: %v", len([]rune(bad)), err)
		}
	}

	// A cancelled or deleted event takes no photos.
	if _, err := f.pool.Exec(ctx, `UPDATE events SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, f.event.PG()); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.add(ctx, t, f.attendee), CodePhotoEventClosed)
}

// core_app cannot move a photo to another event or file, or change its
// uploader or caption.
func TestEventPhotoFrozen(t *testing.T) {
	ctx := context.Background()
	f := newAlbumFixture(t)
	if err := f.add(ctx, t, f.attendee); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE event_photos SET event_id = gen_random_uuid()`,
		`UPDATE event_photos SET upload_id = gen_random_uuid()`,
		`UPDATE event_photos SET uploader_id = gen_random_uuid()`,
		`UPDATE event_photos SET caption = 'başka'`,
	} {
		_, err := f.pool.Exec(ctx, sql)
		wantCode(t, err, "42501")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE event_photos SET deleted_at = now(), deleted_by = $1`, id.New().PG()); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
}

// 30 per person and 500 per album, also when transactions race: the
// trigger locks the event row.
func TestEventPhotoLimitsConcurrent(t *testing.T) {
	ctx := context.Background()
	f := newAlbumFixture(t)
	for range 28 {
		if err := f.add(ctx, t, f.attendee); err != nil {
			t.Fatal(err)
		}
	}
	const n = 8
	uploads := make([]id.ID, n)
	for i := range uploads {
		uploads[i] = readyUpload(t, f.q, f.attendee, uploadOpts{purpose: "event_photo", tayfa: f.tayfa})
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
				return f.insert(ctx, New(tx), f.attendee, uploads[i], true)
			})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch SQLState(err) {
		case "":
			ok++
		case CodePhotoLimit:
		default:
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM event_photos WHERE deleted_at IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if ok != 2 || count != 30 {
		t.Fatalf("eşzamanlı: %d başarılı, %d fotoğraf", ok, count)
	}
	// A deleted photo frees a place.
	if _, err := f.pool.Exec(ctx, `UPDATE event_photos SET deleted_at = now() WHERE id = (SELECT id FROM event_photos LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.add(ctx, t, f.attendee), "")
	wantCode(t, f.add(ctx, t, f.attendee), CodePhotoLimit)

	// The album: 500 with others' photos (written with triggers off).
	tx, err := adminOf(t, f.pool).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, client_request_id)
		SELECT gen_random_uuid(), $1, gen_random_uuid(), gen_random_uuid(), gen_random_uuid() FROM generate_series(1, 470)`, f.event.PG()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	other := id.New()
	f.rsvp(t, other, "attended")
	wantCode(t, f.add(ctx, t, other), CodePhotoLimit)
}

// Deleting the row for good (purge, account deletion, event cascade) puts
// the file in the delete queue; soft delete does not.
func TestEventPhotoReleasesUpload(t *testing.T) {
	ctx := context.Background()
	f := newAlbumFixture(t)
	if err := f.add(ctx, t, f.attendee); err != nil {
		t.Fatal(err)
	}
	var upload pgtype.UUID
	if err := f.pool.QueryRow(ctx, `SELECT upload_id FROM event_photos`).Scan(&upload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE event_photos SET deleted_at = now()`); err != nil {
		t.Fatal(err)
	}
	if s := uploadStatus(t, f.pool, id.ID(upload.Bytes)); s != "ready" {
		t.Fatalf("soft delete: %s", s)
	}
	if err := f.q.DeleteUserEventPhotos(ctx, f.attendee.PG()); err != nil {
		t.Fatal(err)
	}
	if s := uploadStatus(t, f.pool, id.ID(upload.Bytes)); s != "deleting" {
		t.Fatalf("kalıcı silme: %s", s)
	}
	// The upload row goes after its objects; nothing references it.
	if n, err := f.q.DeleteUploadRow(ctx, upload); err != nil || n != 1 {
		t.Fatalf("yükleme satırı: %d %v", n, err)
	}
}
