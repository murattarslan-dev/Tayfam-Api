package event

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	svc    *Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	q      *store.Queries
	// admin is a superuser pool for fixtures core_app cannot write
	// (seeding with triggers off, ADR-0015).
	admin *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	pool := d.App
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(Config{Pool: pool, Log: log})
	must(t, err)
	ts, err := tayfa.New(tayfa.Config{Pool: pool, Log: log})
	must(t, err)
	return &env{svc: svc, tayfas: ts, pool: pool, q: store.New(pool), admin: d.Admin}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func wantField(t *testing.T, what string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Errorf("%s: %v, %q alanında doğrulama hatası bekleniyordu", what, err, field)
	}
}

// newTayfa creates a tayfa through TayfaService (every module on) and
// returns it with its admin.
func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Klasik Otomobil İzmir", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

// seed writes an active membership directly with triggers disabled (the
// test connection is a superuser).
func (e *env) seed(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	return e.seedAs(t, tf, id.New(), role)
}

func (e *env) seedAs(t *testing.T, tf, u id.ID, role authz.Role) id.ID {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: role.String(), Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) mute(t *testing.T, tf, u id.ID) {
	t.Helper()
	e.exec(t, `UPDATE memberships SET muted_until = now() + interval '1 hour', muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG(), id.New().PG())
}

func (e *env) allowMembers(t *testing.T, tf, admin id.ID) {
	t.Helper()
	_, err := e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
}

var izmir = &Point{Lat: 38.4192, Lng: 27.1287}

func inPerson() Location {
	return Location{Mode: ModeInPerson, MeetingPointName: "Kordon", MeetingPoint: izmir, City: "İzmir", District: "Konak"}
}

// input is a valid general, in-person event starting tomorrow.
func input() Input {
	start := time.Now().Add(24 * time.Hour)
	return Input{
		Type: TypeGeneral, Title: "Pazar buluşması", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location: inPerson(), ClientRequestID: id.New().String(),
	}
}

func (e *env) create(t *testing.T, tf, user id.ID, in Input) Event {
	t.Helper()
	v, err := e.svc.Create(ctx, user, tf, in)
	must(t, err)
	return v
}

func withCapacity(n int32) Input {
	in := input()
	in.Capacity = n
	return in
}

// ---------------------------------------------------------------------------

func TestCreateAndGet(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := input()
	in.Type = TypeConvoy
	in.Title = "  Ege turu  "
	in.Description = "Satır 1\nSatır 2"
	in.Capacity = 10
	v := e.create(t, tf, admin, in)

	if v.Title != "Ege turu" || v.Type != TypeConvoy || v.Status != StatusScheduled || v.Capacity != 10 ||
		v.CreatedBy != admin || v.TayfaID != tf || !v.CallerCanManage || v.MeetingPoint == nil || *v.MeetingPoint != *izmir ||
		v.City != "İzmir" || v.District != "Konak" || v.OnlineURL != "" {
		t.Fatalf("etkinlik = %+v", v)
	}
	// Audit record and event carry IDs and the type only: no title, place
	// or coordinates.
	var meta, payload string
	must(t, e.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_logs WHERE action = 'event.created'`).Scan(&meta))
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'event.created'`).Scan(&payload))
	for _, s := range []string{meta, payload} {
		for _, secret := range []string{"Ege", "Kordon", "İzmir", "Konak", "38.4", "27.1"} {
			if strings.Contains(s, secret) {
				t.Errorf("kayıtta kişisel/içerik verisi: %s", s)
			}
		}
	}
	if !strings.Contains(payload, tf.String()) || !strings.Contains(payload, v.ID.String()) {
		t.Errorf("olay = %s", payload)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.created' AND tayfa_id = $1 AND subject_id = $2 AND actor_id = $3`, tf.PG(), v.ID.PG(), admin.PG()); n != 1 {
		t.Errorf("olayın tayfa/konu/aktör sütunları = %d", n)
	}

	// The same client_request_id returns the same event.
	again, err := e.svc.Create(ctx, admin, tf, in)
	must(t, err)
	if again.ID != v.ID || e.count(t, `SELECT count(*) FROM events`) != 1 {
		t.Fatal("tekrar gönderim ikinci etkinlik açtı")
	}
	// ... but not in another tayfa.
	other, _ := e.newTayfa(t)
	e.seedAs(t, other, admin, authz.Admin)
	_, err = e.svc.Create(ctx, admin, other, in)
	wantField(t, "başka tayfada aynı istek kimliği", err, "client_request_id")

	// Every Faz 1 type is created at the catalog's version (the second for
	// all but fishing since issue #260); none has a detail table any more
	// (issue #243).
	for _, typ := range Types {
		in := input()
		in.Type = typ
		switch typ {
		case TypeCamp:
			in.Fields = values("site_name", `"Kazdağları"`, "has_water", `true`, "has_toilet", `false`, "notes", `"Odun getirin"`)
		case TypeGaming:
			in.Fields = values("game", `"CS2"`, "platform", `"pc"`)
		case TypeSports:
			in.Fields = values("sport", `"football"`, "venue_name", `"Halısaha"`)
		case TypeHike:
			in.Fields = values("difficulty", `3`, "planned_distance", `12.345`, "planned_elevation", `800`)
		}
		v := e.create(t, tf, admin, in)
		got, err := e.svc.Get(ctx, admin, tf, v.ID)
		must(t, err)
		wantVersion := int32(2)
		if typ == TypeFishing {
			wantVersion = 1
		}
		if got.Type != typ || got.TypeVersion != wantVersion || got.Fields == nil || len(got.Fields) != len(in.Fields) {
			t.Errorf("%s: tür %q sürüm %d alanlar %v", typ, got.Type, got.TypeVersion, got.Fields)
		}
	}
	// A hike's Faz 1 details are fields (issue #242); the distance keeps
	// two decimals.
	hike := input()
	hike.Type = TypeHike
	hike.Fields = values("difficulty", `5`, "planned_distance", `12.345`)
	got, err := e.svc.Get(ctx, admin, tf, e.create(t, tf, admin, hike).ID)
	must(t, err)
	wantValues(t, "yürüyüş alanları", got.Fields, "difficulty", `5`, "planned_distance", `12.35`)
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	for _, tc := range []struct {
		name  string
		edit  func(*Input)
		field string
	}{
		{"bilinmeyen tür", func(i *Input) { i.Type = "paragliding" }, "type"},
		{"boş başlık", func(i *Input) { i.Title = "   " }, "title"},
		{"uzun başlık", func(i *Input) { i.Title = strings.Repeat("ğ", 121) }, "title"},
		{"kontrol karakteri", func(i *Input) { i.Title = "a\x00b" }, "title"},
		{"uzun açıklama", func(i *Input) { i.Description = strings.Repeat("ş", 5001) }, "description"},
		{"geçmişte başlangıç", func(i *Input) { i.StartsAt = time.Now().Add(-time.Hour) }, "start_time"},
		{"çok ileri başlangıç", func(i *Input) { i.StartsAt = time.Now().AddDate(3, 0, 0); i.EndsAt = i.StartsAt.Add(time.Hour) }, "start_time"},
		{"bitiş başlangıçtan önce", func(i *Input) { i.EndsAt = i.StartsAt }, "end_time"},
		{"31 günlük etkinlik", func(i *Input) { i.EndsAt = i.StartsAt.Add(31 * 24 * time.Hour) }, "end_time"},
		{"belirsiz biçim", func(i *Input) { i.Location.Mode = "" }, "mode"},
		{"konumsuz yüz yüze", func(i *Input) { i.Location.MeetingPoint = nil }, "meeting_point"},
		{"enlem 91", func(i *Input) { i.Location.MeetingPoint = &Point{Lat: 91, Lng: 0} }, "meeting_point"},
		{"boylam -181", func(i *Input) { i.Location.MeetingPoint = &Point{Lat: 0, Lng: -181} }, "meeting_point"},
		{"ilsiz yüz yüze", func(i *Input) { i.Location.City = "" }, "city"},
		{"ilçesiz yüz yüze", func(i *Input) { i.Location.District = " " }, "district"},
		{"yüz yüzede bağlantı", func(i *Input) { i.Location.OnlineURL = "https://example.com" }, "online_url"},
		{"online'da konum", func(i *Input) { i.Location.Mode = ModeOnline; i.Location.OnlineURL = "https://example.com" }, "meeting_point"},
		{"http bağlantı", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "http://example.com"} }, "online_url"},
		{"javascript bağlantı", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "javascript:alert(1)"} }, "online_url"},
		{"büyük harf şema", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "HTTPS://example.com"} }, "online_url"},
		{"kullanıcı bilgili bağlantı", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "https://a:b@example.com"} }, "online_url"},
		{"hostsuz bağlantı", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "https:///yol"} }, "online_url"},
		{"boşluklu bağlantı", func(i *Input) { i.Location = Location{Mode: ModeOnline, OnlineURL: "https://example.com/a b"} }, "online_url"},
		{"negatif kapasite", func(i *Input) { i.Capacity = -1 }, "capacity"},
		{"büyük kapasite", func(i *Input) { i.Capacity = 10001 }, "capacity"},
		{"uzun kamp notu", func(i *Input) { i.Type = TypeCamp; i.Fields = values("notes", `"`+strings.Repeat("n", 1001)+`"`) }, "fields.notes"},
		{"metin su", func(i *Input) { i.Type = TypeCamp; i.Fields = values("has_water", `"evet"`) }, "fields.has_water"},
		{"bozuk tür anahtarı", func(i *Input) { i.Type = "Hike Night" }, "type"},
		{"alansız oyun", func(i *Input) { i.Type = TypeGaming }, "fields.game"},
		{"alansız spor", func(i *Input) { i.Type = TypeSports }, "fields.sport"},
		{"bilinmeyen platform", func(i *Input) { i.Type = TypeGaming; i.Fields = values("game", `"x"`, "platform", `"amiga"`) }, "fields.platform"},
		{"uzun spor dalı", func(i *Input) { i.Type = TypeSports; i.Fields = values("sport", `"`+strings.Repeat("g", 61)+`"`) }, "fields.sport"},
		{"türde olmayan alan", func(i *Input) { i.Fields = values("stake", `100`) }, "fields"},
		{"zorluk 6", func(i *Input) { i.Type = TypeHike; i.Fields = values("difficulty", `6`) }, "fields.difficulty"},
		{"sıfır mesafe", func(i *Input) { i.Type = TypeHike; i.Fields = values("planned_distance", `0`) }, "fields.planned_distance"},
		{"irtifa 9001", func(i *Input) { i.Type = TypeHike; i.Fields = values("planned_elevation", `9001`) }, "fields.planned_elevation"},
		{"istek kimliği yok", func(i *Input) { i.ClientRequestID = "" }, "client_request_id"},
	} {
		in := input()
		tc.edit(&in)
		_, err := e.svc.Create(ctx, admin, tf, in)
		wantField(t, tc.name, err, tc.field)
	}
	if n := e.count(t, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("%d geçersiz etkinlik yazıldı", n)
	}

	// A convoy without details and an online event are fine.
	in := input()
	in.Type = TypeConvoy
	e.create(t, tf, admin, in)
	in = input()
	in.Location = Location{Mode: ModeOnline, OnlineURL: "https://discord.gg/ornek"}
	v := e.create(t, tf, admin, in)
	if v.OnlineURL != "https://discord.gg/ornek" || v.MeetingPoint != nil || v.City != "" {
		t.Fatalf("online = %+v", v)
	}
}

// TestCreatePermission: by default moderators and admins create events; the
// tayfa setting opens it to every active member; muted members never.
func TestCreatePermission(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	mod := e.seed(t, tf, authz.Moderator)
	member := e.seed(t, tf, authz.Member)
	muted := e.seed(t, tf, authz.Member)
	e.mute(t, tf, muted)

	_, err := e.svc.Create(ctx, member, tf, input())
	wantErr(t, "ayar kapalı, üye", err, apperr.ErrPermissionDenied)
	e.create(t, tf, mod, input())
	e.create(t, tf, admin, input())

	e.allowMembers(t, tf, admin)
	e.create(t, tf, member, input())
	_, err = e.svc.Create(ctx, muted, tf, input())
	wantErr(t, "ayar açık, susturulmuş üye", err, apperr.ErrMuted)

	// Turning it off again: the member's own event stays theirs to manage.
	_, err = e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}})
	must(t, err)
	_, err = e.svc.Create(ctx, member, tf, input())
	wantErr(t, "ayar tekrar kapalı", err, apperr.ErrPermissionDenied)
}

func TestOrganizerAndModerators(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	mod := e.seed(t, tf, authz.Moderator)
	org := e.seed(t, tf, authz.Member)
	other := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, org, input())
	modEvent := e.create(t, tf, mod, input())

	title := Input{Title: "Yeni başlık"}
	_, err := e.svc.Update(ctx, other, tf, ev.ID, []string{FieldTitle}, title)
	wantErr(t, "başka üye düzenler", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Cancel(ctx, other, tf, ev.ID, "")
	wantErr(t, "başka üye iptal eder", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Update(ctx, org, tf, modEvent.ID, []string{FieldTitle}, title)
	wantErr(t, "organizatör başkasının etkinliğini düzenler", err, apperr.ErrPermissionDenied)

	got, err := e.svc.Get(ctx, other, tf, ev.ID)
	must(t, err)
	if got.CallerCanManage {
		t.Error("başka üyeye yönetim gösterildi")
	}

	v, err := e.svc.Update(ctx, org, tf, ev.ID, []string{FieldTitle}, title)
	must(t, err)
	if v.Title != "Yeni başlık" || !v.CallerCanManage {
		t.Fatalf("organizatör = %+v", v)
	}
	_, err = e.svc.Update(ctx, mod, tf, ev.ID, []string{FieldDescription}, Input{Description: "moderatör"})
	must(t, err)
	_, err = e.svc.Update(ctx, admin, tf, modEvent.ID, []string{FieldDiscoverable}, Input{Discoverable: true})
	must(t, err)

	// A muted organizer cannot edit their event.
	e.mute(t, tf, org)
	_, err = e.svc.Update(ctx, org, tf, ev.ID, []string{FieldTitle}, title)
	wantErr(t, "susturulmuş organizatör", err, apperr.ErrMuted)
}

func TestUpdate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := input()
	in.Type = TypeFishing
	ev := e.create(t, tf, admin, in)

	start := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	v, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTime, FieldLocation, FieldCapacity}, Input{
		StartsAt: start, EndsAt: start.Add(time.Hour), Capacity: 5,
		Location: Location{Mode: ModeOnline, OnlineURL: "https://example.com/oda"},
		Title:    "maskede yok",
	})
	must(t, err)
	if !v.StartsAt.Equal(start) || v.Mode != ModeOnline || v.MeetingPoint != nil || v.City != "" || v.Capacity != 5 || v.Title != ev.Title {
		t.Fatalf("güncel = %+v", v)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'event.updated'`).Scan(&payload))
	if !strings.Contains(payload, `"fields": ["time", "location", "capacity"]`) || strings.Contains(payload, "example.com") {
		t.Fatalf("olay = %s", payload)
	}

	for _, tc := range []struct {
		fields []string
		in     Input
		field  string
	}{
		{nil, Input{}, "update_mask"},
		{[]string{"type"}, Input{}, "update_mask"},
		{[]string{"status"}, Input{}, "update_mask"},
		{[]string{FieldTitle}, Input{}, "title"},
		{[]string{FieldLocation}, Input{Location: Location{Mode: ModeOnline}}, "online_url"},
		// No type has Faz 1 details any more (issue #243).
		{[]string{"details"}, Input{}, "update_mask"},
	} {
		_, err := e.svc.Update(ctx, admin, tf, ev.ID, tc.fields, tc.in)
		wantField(t, fmt.Sprint(tc.fields), err, tc.field)
	}
	general := e.create(t, tf, admin, input())

	// Ended events no longer change.
	e.exec(t, `UPDATE events SET starts_at = now() - interval '3 hours', ends_at = now() - interval '1 hour' WHERE id = $1`, general.ID.PG())
	_, err = e.svc.Update(ctx, admin, tf, general.ID, []string{FieldTitle}, Input{Title: "x"})
	wantErr(t, "bitmiş etkinlik", err, apperr.ErrEventEnded)
	_, err = e.svc.SetRSVP(ctx, admin, tf, general.ID, RSVPGoing)
	wantErr(t, "bitmiş etkinliğe katılım", err, apperr.ErrEventEnded)
	_, err = e.svc.Cancel(ctx, admin, tf, general.ID, "")
	wantErr(t, "bitmiş etkinliği iptal", err, apperr.ErrEventEnded)
}

func TestCancel(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, admin, input())
	_, err := e.svc.SetRSVP(ctx, member, tf, ev.ID, RSVPGoing)
	must(t, err)

	_, err = e.svc.Cancel(ctx, admin, tf, ev.ID, strings.Repeat("a", 501))
	wantField(t, "uzun neden", err, "reason")

	v, err := e.svc.Cancel(ctx, admin, tf, ev.ID, "Fırtına uyarısı")
	must(t, err)
	if v.Status != StatusCancelled || v.CancelReason != "Fırtına uyarısı" || v.CancelledAt.IsZero() {
		t.Fatalf("iptal = %+v", v)
	}
	// Not deleted: members still see it, with its RSVPs.
	got, err := e.svc.Get(ctx, member, tf, ev.ID)
	must(t, err)
	if got.Status != StatusCancelled || got.Going != 1 || got.CallerRSVP != RSVPGoing {
		t.Fatalf("iptal edilmiş = %+v", got)
	}
	list, _, err := e.svc.ListUpcoming(ctx, member, tf, 0, "")
	must(t, err)
	if len(list) != 1 || list[0].Status != StatusCancelled {
		t.Fatalf("liste = %+v", list)
	}

	_, err = e.svc.SetRSVP(ctx, member, tf, ev.ID, RSVPNotGoing)
	wantErr(t, "iptalden sonra katılım", err, apperr.ErrEventCancelled)
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTitle}, Input{Title: "x"})
	wantErr(t, "iptalden sonra düzenleme", err, apperr.ErrEventCancelled)
	_, err = e.svc.Cancel(ctx, admin, tf, ev.ID, "")
	wantErr(t, "ikinci iptal", err, apperr.ErrEventCancelled)

	var meta, payload string
	must(t, e.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_logs WHERE action = 'event.cancelled'`).Scan(&meta))
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'event.cancelled'`).Scan(&payload))
	if strings.Contains(meta+payload, "Fırtına") {
		t.Fatal("iptal nedeni kayda girdi")
	}
}

func TestRSVPWaitlistAndPromotion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a, b, c, d := e.seed(t, tf, authz.Member), e.seed(t, tf, authz.Member), e.seed(t, tf, authz.Member), e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, admin, withCapacity(2))

	rsvp := func(u id.ID, want, stored string) {
		t.Helper()
		got, err := e.svc.SetRSVP(ctx, u, tf, ev.ID, want)
		must(t, err)
		if got != stored {
			t.Fatalf("%s → %s, %s bekleniyordu", want, got, stored)
		}
	}
	status := func(u id.ID) string {
		t.Helper()
		r, err := e.q.GetRsvp(ctx, store.GetRsvpParams{EventID: ev.ID.PG(), UserID: u.PG()})
		if err != nil {
			return ""
		}
		return r.Status
	}

	_, err := e.svc.SetRSVP(ctx, a, tf, ev.ID, RSVPWaitlist)
	wantField(t, "bekleme listesi istenemez", err, "status")

	rsvp(a, RSVPGoing, RSVPGoing)
	rsvp(b, RSVPGoing, RSVPGoing)
	rsvp(c, RSVPGoing, RSVPWaitlist)
	rsvp(d, RSVPGoing, RSVPWaitlist)
	// Asking again keeps the place in line.
	rsvp(c, RSVPGoing, RSVPWaitlist)
	rsvp(a, RSVPGoing, RSVPGoing)

	v, err := e.svc.Get(ctx, a, tf, ev.ID)
	must(t, err)
	if v.Going != 2 || v.Waitlist != 2 || v.CallerRSVP != RSVPGoing {
		t.Fatalf("sayılar = %+v", v)
	}
	// Waitlist order is the order of arrival.
	list, _, err := e.svc.ListAttendees(ctx, a, tf, ev.ID, RSVPWaitlist, 0, "")
	must(t, err)
	if len(list) != 2 || list[0].UserID != c || list[1].UserID != d {
		t.Fatalf("bekleme listesi = %+v", list)
	}

	// a leaves: c (oldest in line) becomes going in the same transaction.
	rsvp(a, RSVPMaybe, RSVPMaybe)
	if status(c) != RSVPGoing || status(d) != RSVPWaitlist {
		t.Fatalf("terfi: c=%s d=%s", status(c), status(d))
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'rsvp.promoted'`).Scan(&payload))
	if !strings.Contains(payload, c.String()) || !strings.Contains(payload, ev.ID.String()) {
		t.Fatalf("terfi olayı = %s", payload)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'rsvp.promoted'`); n != 1 {
		t.Fatalf("terfi denetim kaydı = %d", n)
	}

	// Leaving the waitlist promotes nobody.
	rsvp(d, RSVPNotGoing, RSVPNotGoing)
	// Raising the capacity promotes; maybe → going fits now.
	rsvp(a, RSVPGoing, RSVPWaitlist)
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCapacity}, Input{Capacity: 3})
	must(t, err)
	if status(a) != RSVPGoing {
		t.Fatalf("kapasite artınca terfi yok: %s", status(a))
	}
	// Lowering it demotes nobody.
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCapacity}, Input{Capacity: 1})
	must(t, err)
	v, err = e.svc.Get(ctx, a, tf, ev.ID)
	must(t, err)
	if v.Going != 3 {
		t.Fatalf("kapasite düşünce katılımcı azaldı: %d", v.Going)
	}
	// Removing the limit promotes everyone waiting.
	rsvp(d, RSVPGoing, RSVPWaitlist)
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCapacity}, Input{Capacity: 0})
	must(t, err)
	if status(d) != RSVPGoing {
		t.Fatalf("sınır kalkınca terfi yok: %s", status(d))
	}

	// RSVP events carry IDs and the status only.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'rsvp.changed'`); n < 6 {
		t.Fatalf("rsvp.changed = %d", n)
	}
}

// TestRSVPConcurrentLastSeat: two members ask for the last seat at the same
// time; exactly one gets it.
func TestRSVPConcurrentLastSeat(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	for round := range 5 {
		ev := e.create(t, tf, admin, withCapacity(1))
		users := []id.ID{e.seed(t, tf, authz.Member), e.seed(t, tf, authz.Member)}
		var wg sync.WaitGroup
		results := make([]string, len(users))
		errs := make([]error, len(users))
		start := make(chan struct{})
		for i, u := range users {
			wg.Go(func() {
				<-start
				results[i], errs[i] = e.svc.SetRSVP(ctx, u, tf, ev.ID, RSVPGoing)
			})
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			must(t, err)
		}
		going := 0
		for _, r := range results {
			if r == RSVPGoing {
				going++
			}
		}
		if going != 1 || e.count(t, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND status = 'going'`, ev.ID.PG()) != 1 {
			t.Fatalf("tur %d: sonuçlar %v", round, results)
		}
	}
}

func TestModuleDisabled(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, admin, input())
	must(t, e.tayfas.SetModule(ctx, admin, tf, "events", false))

	for name, err := range e.callAll(member, tf, ev.ID) {
		wantErr(t, name, err, apperr.ErrModuleDisabled)
	}
	for name, err := range e.callAll(admin, tf, ev.ID) {
		wantErr(t, "yönetici "+name, err, apperr.ErrModuleDisabled)
	}
	// Non-members learn nothing, not even that the module is off.
	for name, err := range e.callAll(id.New(), tf, ev.ID) {
		wantErr(t, "üye değil "+name, err, apperr.ErrTayfaNotFound)
	}

	must(t, e.tayfas.SetModule(ctx, admin, tf, "events", true))
	_, err := e.svc.Get(ctx, member, tf, ev.ID)
	must(t, err)
}

// callAll calls every method once.
func (e *env) callAll(user, tf, ev id.ID) map[string]error {
	out := map[string]error{}
	_, out["Create"] = e.svc.Create(ctx, user, tf, input())
	_, out["Update"] = e.svc.Update(ctx, user, tf, ev, []string{FieldTitle}, Input{Title: "x"})
	_, out["Cancel"] = e.svc.Cancel(ctx, user, tf, ev, "")
	_, out["Get"] = e.svc.Get(ctx, user, tf, ev)
	_, _, out["ListUpcoming"] = e.svc.ListUpcoming(ctx, user, tf, 0, "")
	_, out["SetRSVP"] = e.svc.SetRSVP(ctx, user, tf, ev, RSVPGoing)
	_, _, out["ListAttendees"] = e.svc.ListAttendees(ctx, user, tf, ev, "", 0, "")
	_, out["CheckIn"] = e.svc.CheckIn(ctx, user, tf, ev, izmir)
	out["SetAttendance"] = e.svc.SetAttendance(ctx, user, tf, ev, id.New(), true)
	_, out["AddPhoto"] = e.svc.AddPhoto(ctx, user, tf, ev, PhotoInput{UploadID: id.New(), ClientRequestID: id.New().String()})
	_, _, out["ListPhotos"] = e.svc.ListPhotos(ctx, user, tf, ev, 0, "")
	out["RemovePhoto"] = e.svc.RemovePhoto(ctx, user, tf, ev, id.New())
	return out
}

// TestIDOR: an event is only reachable through its own tayfa, and only by
// its active members.
func TestIDOR(t *testing.T) {
	e := setup(t)
	tfA, adminA := e.newTayfa(t)
	tfB, adminB := e.newTayfa(t)
	evA := e.create(t, tfA, adminA, input())
	// adminB is an admin of B and not a member of A.
	for name, err := range e.callAll(adminB, tfA, evA.ID) {
		wantErr(t, "başka tayfanın yöneticisi "+name, err, apperr.ErrTayfaNotFound)
	}
	// Through B, A's event does not exist.
	for name, err := range e.callAll(adminB, tfB, evA.ID) {
		if name == "Create" || name == "ListUpcoming" {
			must(t, err)
			continue
		}
		wantErr(t, "B üzerinden A'nın etkinliği "+name, err, apperr.ErrEventNotFound)
	}
	list, _, err := e.svc.ListUpcoming(ctx, adminB, tfB, 0, "")
	must(t, err)
	for _, v := range list {
		if v.ID == evA.ID {
			t.Fatal("A'nın etkinliği B'nin listesinde")
		}
	}
	// A deleted event is gone.
	e.exec(t, `UPDATE events SET deleted_at = now() WHERE id = $1`, evA.ID.PG())
	_, err = e.svc.Get(ctx, adminA, tfA, evA.ID)
	wantErr(t, "silinmiş etkinlik", err, apperr.ErrEventNotFound)

	// Pending and banned members see nothing.
	ev := e.create(t, tfA, adminA, input())
	for _, st := range []string{"pending", "banned"} {
		u := e.seed(t, tfA, authz.Member)
		e.exec(t, `UPDATE memberships SET status = $3, joined_at = NULL WHERE tayfa_id = $1 AND user_id = $2`, tfA.PG(), u.PG(), st)
		_, err := e.svc.Get(ctx, u, tfA, ev.ID)
		wantErr(t, st, err, apperr.ErrTayfaNotFound)
	}
}

func TestReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	for name, err := range e.callAll(admin, tf, ev.ID) {
		switch name {
		case "Get", "ListUpcoming", "ListAttendees", "ListPhotos":
			must(t, err)
		default:
			wantErr(t, name, err, apperr.ErrTayfaReadOnly)
		}
	}
}

func TestListUpcomingPaging(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	var want []id.ID
	for i := range 5 {
		in := input()
		in.StartsAt = time.Now().Add(time.Duration(5-i) * time.Hour)
		in.EndsAt = in.StartsAt.Add(time.Hour)
		want = append([]id.ID{e.create(t, tf, admin, in).ID}, want...)
	}
	ended := e.create(t, tf, admin, input())
	e.exec(t, `UPDATE events SET starts_at = now() - interval '3 hours', ends_at = now() - interval '1 hour' WHERE id = $1`, ended.ID.PG())

	var got []id.ID
	token := ""
	for page := 0; ; page++ {
		list, next, err := e.svc.ListUpcoming(ctx, admin, tf, 2, token)
		must(t, err)
		for _, v := range list {
			got = append(got, v.ID)
		}
		if next == "" {
			break
		}
		if page > 5 {
			t.Fatal("sayfalama bitmiyor")
		}
		token = next
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sıra = %v, %v bekleniyordu", got, want)
	}

	_, _, err := e.svc.ListUpcoming(ctx, admin, tf, 2, "bozuk")
	wantField(t, "bozuk token", err, "page_token")
	_, next, err := e.svc.ListUpcoming(ctx, admin, tf, 1, "")
	must(t, err)
	_, _, err = e.svc.ListAttendees(ctx, admin, tf, want[0], "", 1, next)
	wantField(t, "başka metodun tokenı", err, "page_token")
	_, _, err = e.svc.ListUpcoming(ctx, admin, tf, -1, "")
	wantField(t, "negatif sayfa", err, "page_size")
}

func TestListAttendeesPaging(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	var users []id.ID
	for range 5 {
		u := e.seed(t, tf, authz.Member)
		_, err := e.svc.SetRSVP(ctx, u, tf, ev.ID, RSVPGoing)
		must(t, err)
		users = append(users, u)
	}
	var got []id.ID
	token := ""
	for {
		list, next, err := e.svc.ListAttendees(ctx, users[0], tf, ev.ID, "", 2, token)
		must(t, err)
		for _, a := range list {
			got = append(got, a.UserID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(got) != fmt.Sprint(users) {
		t.Fatalf("sıra = %v, %v bekleniyordu", got, users)
	}
	// A token for one status is not accepted for another.
	_, next, err := e.svc.ListAttendees(ctx, admin, tf, ev.ID, RSVPGoing, 1, "")
	must(t, err)
	_, _, err = e.svc.ListAttendees(ctx, admin, tf, ev.ID, RSVPMaybe, 1, next)
	wantField(t, "başka durumun tokenı", err, "page_token")
	_, _, err = e.svc.ListAttendees(ctx, admin, tf, ev.ID, "attended", 1, "")
	wantField(t, "bilinmeyen durum", err, "status")

	// Muted members still read.
	muted := e.seed(t, tf, authz.Member)
	e.mute(t, tf, muted)
	_, _, err = e.svc.ListAttendees(ctx, muted, tf, ev.ID, "", 0, "")
	must(t, err)
	_, err = e.svc.Get(ctx, muted, tf, ev.ID)
	must(t, err)
	_, err = e.svc.SetRSVP(ctx, muted, tf, ev.ID, RSVPGoing)
	wantErr(t, "susturulmuş katılım", err, apperr.ErrMuted)
}
