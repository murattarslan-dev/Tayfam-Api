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
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(Config{Pool: pool, Log: log})
	must(t, err)
	ts, err := tayfa.New(tayfa.Config{Pool: pool, Log: log})
	must(t, err)
	return &env{svc: svc, tayfas: ts, pool: pool, q: store.New(pool)}
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
	tx, err := e.pool.Begin(ctx)
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
	tx, err := e.pool.Begin(ctx)
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
	in.Details = Details{Convoy: &Convoy{Route: []Point{{38.4, 27.1}, {38.3, 26.3}, {37.9, 27.3}}}}
	v := e.create(t, tf, admin, in)

	if v.Title != "Ege turu" || v.Type != TypeConvoy || v.Status != StatusScheduled || v.Capacity != 10 ||
		v.CreatedBy != admin || v.TayfaID != tf || !v.CallerCanManage || v.MeetingPoint == nil || *v.MeetingPoint != *izmir ||
		v.City != "İzmir" || v.District != "Konak" || v.OnlineURL != "" {
		t.Fatalf("etkinlik = %+v", v)
	}
	if v.Details.Convoy == nil || len(v.Details.Convoy.Route) != 3 || v.Details.Convoy.Route[1] != (Point{38.3, 26.3}) {
		t.Fatalf("detay = %+v", v.Details)
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

	// Every type is created with its detail row.
	for _, typ := range Types {
		in := input()
		in.Type = typ
		switch typ {
		case TypeGaming:
			in.Details.Gaming = &Gaming{Game: "CS2", Platform: "pc"}
		case TypeSports:
			in.Details.Sports = &Sports{Sport: "football", VenueName: "Halısaha"}
		}
		v := e.create(t, tf, admin, in)
		got, err := e.svc.Get(ctx, admin, tf, v.ID)
		must(t, err)
		if want := typ; got.Details.typ() != want && !(typ == TypeGeneral && got.Details.typ() == "") {
			t.Errorf("%s: detay %+v", typ, got.Details)
		}
	}
	got, err := e.svc.Get(ctx, admin, tf, e.create(t, tf, admin, func() Input { i := input(); i.Type = TypeHike; return i }()).ID)
	must(t, err)
	if got.Details.Hike.ReturnGraceMinutes != 30 {
		t.Errorf("varsayılan dönüş payı = %d", got.Details.Hike.ReturnGraceMinutes)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	route := func(n int) []Point {
		p := make([]Point, n)
		for i := range p {
			p[i] = Point{Lat: 38, Lng: 27}
		}
		return p
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Input)
		field string
	}{
		{"bilinmeyen tür", func(i *Input) { i.Type = "cycling" }, "type"},
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
		{"genel türde detay", func(i *Input) { i.Details.Camp = &Camp{} }, "details"},
		{"yanlış türde detay", func(i *Input) { i.Type = TypeHike; i.Details.Camp = &Camp{} }, "details"},
		{"iki detay", func(i *Input) { i.Type = TypeCamp; i.Details = Details{Camp: &Camp{}, Hike: &Hike{}} }, "details"},
		{"detaysız oyun", func(i *Input) { i.Type = TypeGaming }, "details"},
		{"detaysız spor", func(i *Input) { i.Type = TypeSports }, "details"},
		{"bilinmeyen platform", func(i *Input) { i.Type = TypeGaming; i.Details.Gaming = &Gaming{Game: "x", Platform: "amiga"} }, "platform"},
		{"bilinmeyen spor", func(i *Input) { i.Type = TypeSports; i.Details.Sports = &Sports{Sport: "golf"} }, "sport"},
		{"tek noktalı rota", func(i *Input) { i.Type = TypeConvoy; i.Details.Convoy = &Convoy{Route: route(1)} }, "route"},
		{"501 noktalı rota", func(i *Input) { i.Type = TypeConvoy; i.Details.Convoy = &Convoy{Route: route(501)} }, "route"},
		{"aralık dışı rota", func(i *Input) {
			i.Type = TypeConvoy
			i.Details.Convoy = &Convoy{Route: []Point{{1, 1}, {1, 200}}}
		}, "route"},
		{"zorluk 6", func(i *Input) { i.Type = TypeHike; i.Details.Hike = &Hike{Difficulty: 6} }, "difficulty"},
		{"başlangıçtan önce dönüş", func(i *Input) {
			i.Type = TypeHike
			i.Details.Hike = &Hike{ExpectedReturnAt: i.StartsAt.Add(-time.Minute)}
		}, "expected_return_time"},
		{"bilinmeyen mera görünürlüğü", func(i *Input) { i.Type = TypeFishing; i.Details.Fishing = &Fishing{SpotVisibility: "public"} }, "spot_visibility"},
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

	// 500 points and an online event are fine.
	in := input()
	in.Type, in.Details.Convoy = TypeConvoy, &Convoy{Route: route(500)}
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
	in.Type, in.Details.Hike = TypeHike, &Hike{Difficulty: 3}
	ev := e.create(t, tf, admin, in)

	start := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	v, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTime, FieldLocation, FieldDetails, FieldCapacity}, Input{
		StartsAt: start, EndsAt: start.Add(time.Hour), Capacity: 5,
		Location: Location{Mode: ModeOnline, OnlineURL: "https://example.com/oda"},
		Details:  Details{Hike: &Hike{Difficulty: 5, DistanceKm: 12.345, ElevationM: 800, ReturnGraceMinutes: 45}},
		Title:    "maskede yok",
	})
	must(t, err)
	if !v.StartsAt.Equal(start) || v.Mode != ModeOnline || v.MeetingPoint != nil || v.City != "" || v.Capacity != 5 || v.Title != ev.Title {
		t.Fatalf("güncel = %+v", v)
	}
	if h := v.Details.Hike; h.Difficulty != 5 || h.DistanceKm != 12.35 || h.ElevationM != 800 || h.ReturnGraceMinutes != 45 {
		t.Fatalf("yürüyüş = %+v", h)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'event.updated'`).Scan(&payload))
	if !strings.Contains(payload, `"fields": ["time", "location", "capacity", "details"]`) || strings.Contains(payload, "example.com") {
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
		{[]string{FieldDetails}, Input{}, "details"},
		{[]string{FieldDetails}, Input{Details: Details{Camp: &Camp{}}}, "details"},
	} {
		_, err := e.svc.Update(ctx, admin, tf, ev.ID, tc.fields, tc.in)
		wantField(t, fmt.Sprint(tc.fields), err, tc.field)
	}
	general := e.create(t, tf, admin, input())
	_, err = e.svc.Update(ctx, admin, tf, general.ID, []string{FieldDetails}, Input{Details: Details{Hike: &Hike{}}})
	wantField(t, "genel etkinliğe detay", err, "details")

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
	_, out["AddCampItem"] = e.svc.AddCampItem(ctx, user, tf, ev, "Çadır", 1, id.ID{})
	out["RemoveCampItem"] = e.svc.RemoveCampItem(ctx, user, tf, ev, id.New())
	_, out["SetVehicle"] = e.svc.SetVehicle(ctx, user, tf, ev, VehicleInput{Make: "Ford"})
	out["RemoveVehicle"] = e.svc.RemoveVehicle(ctx, user, tf, ev, id.New())
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
		case "Get", "ListUpcoming", "ListAttendees":
			must(t, err)
		default:
			wantErr(t, name, err, apperr.ErrTayfaReadOnly)
		}
	}
}

func TestConvoyVehiclesAndPlates(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	org := e.seed(t, tf, authz.Member)
	driver := e.seed(t, tf, authz.Member)
	rider := e.seed(t, tf, authz.Member)
	outsider := e.seed(t, tf, authz.Member)
	in := input()
	in.Type = TypeConvoy
	ev := e.create(t, tf, org, in)

	_, err := e.svc.SetVehicle(ctx, driver, tf, ev.ID, VehicleInput{Make: "Ford", Plate: "35 ABC 123"})
	wantErr(t, "katılmadan araç", err, apperr.ErrEventNotGoing)
	for _, u := range []id.ID{driver, rider} {
		_, err := e.svc.SetRSVP(ctx, u, tf, ev.ID, RSVPGoing)
		must(t, err)
	}
	for _, tc := range []struct {
		in    VehicleInput
		field string
	}{
		{VehicleInput{}, "make"},
		{VehicleInput{Make: "Ford", ModelYear: 1899}, "model_year"},
		{VehicleInput{Make: "Ford", ModelYear: int32(time.Now().Year() + 2)}, "model_year"},
		{VehicleInput{Make: "Ford", Plate: "35-ABC-123"}, "plate"},
		{VehicleInput{Make: "Ford", Plate: "35 ŞĞÜ 12"}, "plate"},
		{VehicleInput{Make: "Ford", Plate: "1234567890ABCDEFG"}, "plate"},
	} {
		_, err := e.svc.SetVehicle(ctx, driver, tf, ev.ID, tc.in)
		wantField(t, fmt.Sprintf("%+v", tc.in), err, tc.field)
	}
	veh, err := e.svc.SetVehicle(ctx, driver, tf, ev.ID, VehicleInput{Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: " 35  abc 123 "})
	must(t, err)
	if veh.Plate != "35 ABC 123" {
		t.Fatalf("plaka = %q", veh.Plate)
	}
	// Setting again replaces the caller's vehicle.
	veh2, err := e.svc.SetVehicle(ctx, driver, tf, ev.ID, VehicleInput{Make: "Ford", Model: "Mustang", ModelYear: 1967, Plate: "35 ABC 123"})
	must(t, err)
	if veh2.ID != veh.ID || e.count(t, `SELECT count(*) FROM event_convoy_vehicles`) != 1 {
		t.Fatal("ikinci araç eklendi")
	}

	plate := func(u id.ID) string {
		t.Helper()
		v, err := e.svc.Get(ctx, u, tf, ev.ID)
		must(t, err)
		if len(v.Details.Convoy.Vehicles) != 1 {
			t.Fatalf("araçlar = %+v", v.Details.Convoy.Vehicles)
		}
		return v.Details.Convoy.Vehicles[0].Plate
	}
	for name, tc := range map[string]struct {
		u    id.ID
		want string
	}{
		"sürücü":      {driver, "35 ABC 123"},
		"katılımcı":   {rider, "35 ABC 123"},
		"organizatör": {org, "35 ABC 123"},
		"katılmayan":  {outsider, ""},
		// Admins see the vehicle but not the plate unless they take part.
		"yönetici": {admin, ""},
	} {
		if got := plate(tc.u); got != tc.want {
			t.Errorf("%s: plaka %q, %q bekleniyordu", name, got, tc.want)
		}
	}
	_, err = e.svc.SetRSVP(ctx, outsider, tf, ev.ID, RSVPMaybe)
	must(t, err)
	if plate(outsider) != "" {
		t.Error("belki diyen plakayı gördü")
	}

	// The plate never reaches audit records or events.
	var all string
	must(t, e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(metadata::text, ' '), '') FROM audit_logs`).Scan(&all))
	var events string
	must(t, e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(payload::text, ' '), '') FROM domain_events`).Scan(&events))
	if strings.Contains(all+events, "ABC") || strings.Contains(all+events, "Mustang") {
		t.Fatal("araç bilgisi kayda girdi")
	}

	// Removing: not someone else's (unless managing), always one's own.
	err = e.svc.RemoveVehicle(ctx, rider, tf, ev.ID, veh.ID)
	wantErr(t, "başkasının aracı", err, apperr.ErrPermissionDenied)
	err = e.svc.RemoveVehicle(ctx, rider, tf, ev.ID, id.New())
	wantErr(t, "olmayan araç", err, apperr.ErrEventItemNotFound)
	must(t, e.svc.RemoveVehicle(ctx, org, tf, ev.ID, veh.ID))
	_, err = e.svc.SetVehicle(ctx, driver, tf, ev.ID, VehicleInput{Make: "Ford", Plate: "35 ABC 123"})
	must(t, err)

	// Leaving the convoy takes the vehicle (and plate) with it.
	_, err = e.svc.SetRSVP(ctx, driver, tf, ev.ID, RSVPNotGoing)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_convoy_vehicles`); n != 0 {
		t.Fatalf("ayrılan sürücünün aracı kaldı: %d", n)
	}

	// Vehicles are convoy-only.
	other := e.create(t, tf, org, input())
	_, err = e.svc.SetVehicle(ctx, driver, tf, other.ID, VehicleInput{Make: "Ford"})
	wantErr(t, "genel etkinliğe araç", err, apperr.ErrEventTypeMismatch)
}

func TestCampItems(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	in := input()
	in.Type = TypeCamp
	yes := true
	in.Details.Camp = &Camp{SiteName: "Kazdağları", HasWater: &yes, Notes: "Odun getirin"}
	ev := e.create(t, tf, admin, in)

	_, err := e.svc.AddCampItem(ctx, member, tf, ev.ID, "Çadır", 1, id.ID{})
	wantErr(t, "üye listeyi değiştirir", err, apperr.ErrPermissionDenied)
	_, err = e.svc.AddCampItem(ctx, admin, tf, ev.ID, "", 1, id.ID{})
	wantField(t, "boş ad", err, "name")
	_, err = e.svc.AddCampItem(ctx, admin, tf, ev.ID, "Çadır", 1000, id.ID{})
	wantField(t, "1000 adet", err, "quantity")
	_, err = e.svc.AddCampItem(ctx, admin, tf, ev.ID, "Çadır", 1, id.New())
	wantField(t, "üye olmayana atama", err, "assigned_user_id")

	item, err := e.svc.AddCampItem(ctx, admin, tf, ev.ID, " Çadır ", 0, member)
	must(t, err)
	if item.Name != "Çadır" || item.Quantity != 1 || item.AssignedUserID != member {
		t.Fatalf("malzeme = %+v", item)
	}
	v, err := e.svc.Get(ctx, member, tf, ev.ID)
	must(t, err)
	c := v.Details.Camp
	if c.SiteName != "Kazdağları" || c.HasWater == nil || !*c.HasWater || c.HasToilet != nil || len(c.Items) != 1 {
		t.Fatalf("kamp = %+v", c)
	}

	// Details update keeps the item list.
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldDetails}, Input{Details: Details{Camp: &Camp{SiteName: "Assos"}}})
	must(t, err)
	v, err = e.svc.Get(ctx, member, tf, ev.ID)
	must(t, err)
	if v.Details.Camp.SiteName != "Assos" || v.Details.Camp.HasWater != nil || len(v.Details.Camp.Items) != 1 {
		t.Fatalf("kamp = %+v", v.Details.Camp)
	}

	must(t, e.svc.RemoveCampItem(ctx, admin, tf, ev.ID, item.ID))
	err = e.svc.RemoveCampItem(ctx, admin, tf, ev.ID, item.ID)
	wantErr(t, "ikinci silme", err, apperr.ErrEventItemNotFound)

	// The list has a limit.
	for range campItemsMax {
		_, err := e.svc.AddCampItem(ctx, admin, tf, ev.ID, "Su", 1, id.ID{})
		must(t, err)
	}
	_, err = e.svc.AddCampItem(ctx, admin, tf, ev.ID, "Su", 1, id.ID{})
	wantErr(t, "sınır", err, apperr.ErrEventLimit)

	general := e.create(t, tf, admin, input())
	_, err = e.svc.AddCampItem(ctx, admin, tf, general.ID, "Çadır", 1, id.ID{})
	wantErr(t, "genel etkinliğe malzeme", err, apperr.ErrEventTypeMismatch)
}

func TestFishingSpotVisibility(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	org := e.seed(t, tf, authz.Member)
	mod := e.seed(t, tf, authz.Moderator)
	angler := e.seed(t, tf, authz.Member)
	outsider := e.seed(t, tf, authz.Member)
	in := input()
	in.Type, in.Details.Fishing = TypeFishing, &Fishing{SpotVisibility: SpotParticipants}
	ev := e.create(t, tf, org, in)
	_, err := e.svc.SetRSVP(ctx, angler, tf, ev.ID, RSVPMaybe)
	must(t, err)

	for name, tc := range map[string]struct {
		u    id.ID
		sees bool
	}{
		"organizatör": {org, true}, "moderatör": {mod, true}, "yönetici": {admin, true},
		"belki diyen": {angler, true}, "katılmayan": {outsider, false},
	} {
		v, err := e.svc.Get(ctx, tc.u, tf, ev.ID)
		must(t, err)
		list, _, err := e.svc.ListUpcoming(ctx, tc.u, tf, 0, "")
		must(t, err)
		for _, got := range []Event{v, list[0]} {
			if (got.MeetingPoint != nil) != tc.sees || (got.MeetingPointName != "") != tc.sees {
				t.Errorf("%s: mera görünürlüğü %v bekleniyordu: %+v", name, tc.sees, got.Location)
			}
			// Il and ilçe stay visible: they are not the spot.
			if got.City == "" {
				t.Errorf("%s: il gizlendi", name)
			}
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
			if v.Details.typ() != "" {
				t.Error("listede detay")
			}
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
