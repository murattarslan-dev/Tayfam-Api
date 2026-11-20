package event

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Type flags (issue #257, ADR-0037 §1).

const (
	bloodType = "test_flag_blood"
	visitType = "test_flag_visit"
)

// addType adds a test type to the catalog (version 1, both modes).
func (e *env) addType(t *testing.T, key, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
}

func typed(key string) Input {
	in := input()
	in.Type = key
	return in
}

func (e *env) sensitive(t *testing.T, ev id.ID) bool {
	t.Helper()
	var b bool
	must(t, e.pool.QueryRow(ctx, `SELECT event_attendance_sensitive($1)`, ev.PG()).Scan(&b))
	return b
}

// sensitive_attendance: attending writes no activity record (the database
// refuses one too), and only those who correct attendance get the attendee
// list; the counts stay on the event.
func TestSensitiveAttendance(t *testing.T) {
	e := setup(t)
	e.addType(t, bloodType, `{"flags": {"sensitive_attendance": true}}`)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	organizer := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, organizer, typed(bloodType))
	plain := e.create(t, tf, organizer, input())
	if !e.sensitive(t, ev.ID) || e.sensitive(t, plain.ID) || e.sensitive(t, id.New()) {
		t.Fatal("event_attendance_sensitive tür tanımıyla uyuşmuyor")
	}
	donor := e.going(t, tf, ev.ID, authz.Member)
	walkIn := e.seed(t, tf, authz.Member)
	mod := e.seed(t, tf, authz.Moderator)
	c := &clock{t: ev.StartsAt}
	svc := e.clocked(t, c, nil)

	// A check-in and an organizer correction verify attendance on the
	// answer, but write no personal history.
	_, err := svc.CheckIn(ctx, donor, tf, ev.ID, izmir)
	must(t, err)
	// An edit while records would follow it brings none.
	_, err = svc.Update(ctx, organizer, tf, ev.ID, []string{FieldTitle}, Input{Title: "Kan bağışı günü"})
	must(t, err)
	c.t = ev.EndsAt
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, walkIn, true))
	if st, _, _ := e.attendance(t, ev.ID, walkIn); st != AttendanceAttended {
		t.Fatalf("düzeltme = %s", st)
	}
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("hassas katılımın geçmiş kaydı: %d", n)
	}
	// The plain event still writes one.
	other := e.going(t, tf, plain.ID, authz.Member)
	c.t = plain.StartsAt
	_, err = svc.CheckIn(ctx, other, tf, plain.ID, izmir)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE event_id = $1`, plain.ID.PG()); n != 1 {
		t.Fatalf("sıradan etkinliğin geçmiş kaydı: %d", n)
	}

	// The database's second line.
	r, err := e.q.GetEvent(ctx, store.GetEventParams{TayfaID: tf.PG(), ID: ev.ID.PG()})
	must(t, err)
	err = e.q.UpsertActivityRecord(ctx, store.UpsertActivityRecordParams{
		ID: id.New().PG(), UserID: donor.PG(), EventID: r.ID, TayfaID: r.TayfaID,
		EventType: r.Type, Mode: r.Mode, EventTitle: r.Title, City: r.City, District: r.District,
		StartedAt: r.StartsAt, DurationMinutes: 60, AttendanceMethod: MethodLocation,
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "user_activity_records_sensitive" {
		t.Fatalf("veritabanı hassas katılımın geçmiş kaydını kabul etti: %v", err)
	}

	// The list: the organizer and a moderator; nobody else, the donor
	// included. The counts stay on the event.
	listed := 0
	for who, u := range map[string]id.ID{"organizatör": organizer, "moderatör": mod, "yönetici": admin} {
		list, _, err := svc.ListAttendees(ctx, u, tf, ev.ID, RSVPGoing, 0, "")
		must(t, err)
		seen := map[id.ID]bool{}
		for _, a := range list {
			seen[a.UserID] = true
		}
		if !seen[donor] || !seen[walkIn] {
			t.Fatalf("%s: liste %+v", who, list)
		}
		listed = len(list)
	}
	for who, u := range map[string]id.ID{"bağışçı": donor, "katılmayan": e.seed(t, tf, authz.Member)} {
		for _, st := range []string{RSVPGoing, RSVPMaybe, RSVPNotGoing, RSVPWaitlist} {
			_, _, err := svc.ListAttendees(ctx, u, tf, ev.ID, st, 0, "")
			wantErr(t, who+" "+st, err, apperr.ErrAttendeesHidden)
		}
	}
	got, err := svc.Get(ctx, donor, tf, ev.ID)
	must(t, err)
	if int(got.Going) != listed {
		t.Fatalf("katılımcı sayısı = %d, liste %d", got.Going, listed)
	}
	// A plain event's list stays open to members.
	if _, _, err := svc.ListAttendees(ctx, donor, tf, plain.ID, RSVPGoing, 0, ""); err != nil {
		t.Fatalf("sıradan etkinliğin listesi: %v", err)
	}
}

// A sensitive event counts neither for the friendship score (ADR-0030)
// nor as shared event history.
func TestSensitiveAttendanceNotShared(t *testing.T) {
	e := setup(t)
	e.addType(t, bloodType, `{"flags": {"sensitive_attendance": true}}`)
	tf, admin := e.newTayfa(t)
	blood := e.create(t, tf, admin, typed(bloodType))
	a := e.going(t, tf, blood.ID, authz.Member)
	b := e.seed(t, tf, authz.Member)
	_, err := e.svc.SetRSVP(ctx, b, tf, blood.ID, RSVPGoing)
	must(t, err)
	c := &clock{t: blood.StartsAt}
	svc := e.clocked(t, c, nil)
	for _, u := range []id.ID{a, b} {
		_, err := svc.CheckIn(ctx, u, tf, blood.ID, izmir)
		must(t, err)
	}
	now := pgtype.Timestamptz{Time: blood.EndsAt.Add(time.Hour), Valid: true}
	scores := func() int {
		t.Helper()
		must(t, e.q.DeleteAllFriendScores(ctx))
		_, err := e.q.InsertFriendScores(ctx, store.InsertFriendScoresParams{
			Now: now, HalfLifeSeconds: 180 * 24 * 3600, Since: pgtype.Timestamptz{Time: now.Time.Add(-365 * 24 * time.Hour), Valid: true},
		})
		must(t, err)
		return e.count(t, `SELECT count(*) FROM friend_scores`)
	}
	shared := func() bool {
		t.Helper()
		ok, err := e.q.HasSharedEventHistory(ctx, store.HasSharedEventHistoryParams{UserA: a.PG(), UserB: b.PG(), Now: now})
		must(t, err)
		return ok
	}
	if n := scores(); n != 0 {
		t.Fatalf("hassas katılım arkadaşlık puanına girdi: %d", n)
	}
	if shared() {
		t.Fatal("hassas katılım ortak etkinlik geçmişi sayıldı")
	}

	// The same two at a plain event count.
	plain := e.create(t, tf, admin, input())
	for _, u := range []id.ID{a, b} {
		_, err := e.svc.SetRSVP(ctx, u, tf, plain.ID, RSVPGoing)
		must(t, err)
		_, err = svc.CheckIn(ctx, u, tf, plain.ID, izmir)
		must(t, err)
	}
	if n := scores(); n != 1 {
		t.Fatalf("sıradan etkinlikte puan satırı = %d", n)
	}
	if !shared() {
		t.Fatal("sıradan etkinlik ortak geçmiş sayılmadı")
	}
}

// album_notice: a photo needs consent_ack; other types ignore it.
func TestAlbumNotice(t *testing.T) {
	e := setup(t)
	e.addType(t, visitType, `{"flags": {"album_notice": true}}`)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, typed(visitType))
	u := e.going(t, tf, ev.ID, authz.Member)
	c := &clock{t: ev.StartsAt}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
	a := &album{e: e, tf: tf}

	in := PhotoInput{UploadID: a.upload(t, u), ClientRequestID: id.New().String()}
	_, err = svc.AddPhoto(ctx, u, tf, ev.ID, in)
	wantErr(t, "onaysız", err, apperr.ErrAlbumConsentRequired)
	if n := e.count(t, `SELECT count(*) FROM event_photos WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("onaysız fotoğraf eklendi: %d", n)
	}
	in.ConsentAck = true
	p, err := svc.AddPhoto(ctx, u, tf, ev.ID, in)
	must(t, err)
	if p.UploaderID != u {
		t.Fatalf("fotoğraf: %+v", p)
	}

	// A plain event takes a photo without it.
	plain := e.create(t, tf, admin, input())
	_, err = e.svc.SetRSVP(ctx, u, tf, plain.ID, RSVPGoing)
	must(t, err)
	c.t = plain.StartsAt
	_, err = svc.CheckIn(ctx, u, tf, plain.ID, izmir)
	must(t, err)
	_, err = svc.AddPhoto(ctx, u, tf, plain.ID, PhotoInput{UploadID: a.upload(t, u), ClientRequestID: id.New().String()})
	must(t, err)
}
