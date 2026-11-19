package logbook_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/logbook"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. The catalog's fishing has the "catches" log with a location
// kept to oneself by default. booksType has a "books" log without a
// location, two entries per person, next to another component. optType
// has an optional log, off by default, whose locations go to participants
// by default.
const (
	booksType = "test_books"
	booksV1   = `{"components":[{"kind":"log","key":"books","labels":{"tr":"Okunanlar"},"params":{"fields":[` +
		`{"key":"book","kind":"text","required":true,"labels":{"tr":"Kitap"}},` +
		`{"key":"pages","kind":"integer","min":1,"max":5000,"labels":{"tr":"Sayfa"}}],"max_entries":2}},{"kind":"draw"}]}`
	optType = "test_opt_log"
	optV1   = `{"components":[{"kind":"log","key":"sightings","labels":{"tr":"Gözlemler"},"optional":{"default":false},"params":{"fields":[` +
		`{"key":"species","kind":"text","required":true,"labels":{"tr":"Tür"}}],"location":true,"location_default":"participants"}}]}`
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type env struct {
	logs   *logbook.Service
	events *event.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	clock  *clock
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

func constraintOf(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.ConstraintName
	}
	return ""
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &clock{t: time.Now().UTC().Truncate(time.Microsecond)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	events, err := event.New(event.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	e := &env{logs: logbook.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, booksType, booksV1)
	e.addType(t, optType, optV1)
	return e
}

// exec runs a fixture statement as a superuser with triggers off.
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

// addType writes a type as the platform would (the catalog is read-only
// for the service).
func (e *env) addType(t *testing.T, key, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Olta Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

// member writes an active membership directly.
func (e *env) member(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
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

func (e *env) mute(t *testing.T, tf, u id.ID) {
	t.Helper()
	e.exec(t, `UPDATE memberships SET muted_until = now() + interval '30 days', muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG(), id.New().PG())
}

// going adds a member who answered going.
func (e *env) going(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.member(t, tf, role)
	_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
	must(t, err)
	return u
}

// attended adds a member whose attendance was verified.
func (e *env) attended(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.going(t, tf, ev, role)
	e.exec(t, `UPDATE event_rsvps SET attendance = 'attended', attendance_method = 'location', checked_in_at = now()
		WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG())
	return u
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Akşam oltası", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func on(b bool) *bool { return &b }

// changes counts the event's component_changed events for key.
func (e *env) changes(t *testing.T, ev id.ID, key string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

// values builds an entry's values from key, JSON pairs.
func values(kv ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

func catch(species string, p *logbook.Point, vis string) logbook.Input {
	return logbook.Input{Values: values("species", `"`+species+`"`), Location: p, LocationVisibility: vis, ClientRequestID: id.New().String()}
}

// byID returns the entries of a page by ID.
func byID(p logbook.Page) map[id.ID]logbook.Entry {
	out := map[id.ID]logbook.Entry{}
	for _, x := range p.Entries {
		out[x.ID] = x
	}
	return out
}

// The fishing catch log end to end (the catalog's fishing, key "catches"):
// verified attendance, the record's fields, repeats, who sees which
// location, removal, the photo; no value or location in the audit record
// or the outbox.
func TestLogEndToEnd(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeFishing)
	const key = "catches"
	angler := e.going(t, tf, ev.ID, authz.Member)
	partner := e.going(t, tf, ev.ID, authz.Member)
	outsider := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	spot := &logbook.Point{Lat: 38.3512, Lng: 26.7731}

	// Nothing yet: an empty page with the log's settings.
	page, err := e.logs.List(ctx, ref(angler, tf, ev.ID, key), 0, "")
	must(t, err)
	if page.Entries == nil || len(page.Entries) != 0 || !page.LocationEnabled || page.LocationDefault != logbook.LocationSelf ||
		page.MaxEntries != 100 || page.CallerCanAdd {
		t.Fatalf("boş günlük: %+v", page)
	}

	// Only a verified attendance logs.
	_, err = e.logs.Add(ctx, ref(angler, tf, ev.ID, key), catch("Levrek", nil, ""))
	wantErr(t, "katılımı doğrulanmamış", err, apperr.ErrNotAttended)
	e.exec(t, `UPDATE event_rsvps SET attendance = 'attended', attendance_method = 'location', checked_in_at = now()
		WHERE event_id = $1 AND user_id = $2`, ev.ID.PG(), angler.PG())
	page, err = e.logs.List(ctx, ref(angler, tf, ev.ID, key), 0, "")
	must(t, err)
	if !page.CallerCanAdd {
		t.Fatal("katılımı doğrulanmış kişi ekleyemiyor görünüyor")
	}

	for name, tc := range map[string]struct {
		in    logbook.Input
		field string
	}{
		"türsüz":              {logbook.Input{Values: values("weight", `1.5`), ClientRequestID: id.New().String()}, "values.species"},
		"boş tür":             {logbook.Input{Values: values("species", `" "`), ClientRequestID: id.New().String()}, "values.species"},
		"sıfır ağırlık":       {logbook.Input{Values: values("species", `"Levrek"`, "weight", `0`), ClientRequestID: id.New().String()}, "values.weight"},
		"metin adet":          {logbook.Input{Values: values("species", `"Levrek"`, "count", `"üç"`), ClientRequestID: id.New().String()}, "values.count"},
		"tanımda olmayan":     {logbook.Input{Values: values("species", `"Levrek"`, "price", `100`), ClientRequestID: id.New().String()}, "values"},
		"bozuk konum":         {catch("Levrek", &logbook.Point{Lat: 100}, ""), "location"},
		"konumsuz gizlilik":   {catch("Levrek", nil, logbook.LocationTayfa), "location_visibility"},
		"bilinmeyen gizlilik": {catch("Levrek", spot, "public"), "location_visibility"},
		"bozuk istek kimliği": {logbook.Input{Values: values("species", `"Levrek"`), ClientRequestID: "x"}, "client_request_id"},
	} {
		_, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), tc.in)
		wantField(t, name, err, tc.field)
	}

	before := e.changes(t, ev.ID, key)
	shared := catch("Levrek", spot, logbook.LocationParticipants)
	shared.Values = values("species", `"  Levrek "`, "count", `2`, "weight", `1.256`, "length_cm", `42.46`)
	first, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), shared)
	must(t, err)
	var weight float64
	must(t, json.Unmarshal(first.Values["weight"], &weight))
	if string(first.Values["species"]) != `"Levrek"` || weight != 1.26 || first.Location == nil ||
		first.LocationVisibility != logbook.LocationParticipants || first.UserID != angler || !first.CallerCanRemove {
		t.Fatalf("kayıt: %+v", first)
	}
	again, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), shared)
	must(t, err)
	if again.ID != first.ID || e.count(t, `SELECT count(*) FROM event_log_entries`) != 1 {
		t.Fatal("tekrar gönderim ikinci kayıt açtı")
	}
	private, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), catch("Çipura", spot, ""))
	must(t, err)
	if private.LocationVisibility != logbook.LocationSelf {
		t.Fatalf("varsayılan gizlilik = %q", private.LocationVisibility)
	}
	public, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), catch("Lüfer", spot, logbook.LocationTayfa))
	must(t, err)
	photo := mediatest.Ready(t, e.pool, angler, media.PurposeLogPhoto, tf)
	plain := catch("İstavrit", nil, "")
	plain.PhotoUploadID = photo
	noSpot, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), plain)
	must(t, err)
	if noSpot.Location != nil || noSpot.LocationVisibility != "" || noSpot.PhotoUploadID != photo {
		t.Fatalf("konumsuz kayıt: %+v", noSpot)
	}
	if got := e.changes(t, ev.ID, key); got != before+4 {
		t.Fatalf("canlı güncelleme = %d, %d bekleniyordu", got, before+4)
	}
	// A repeat returns the first entry, whatever it carries.
	reuse := catch("Levrek", nil, "")
	reuse.ClientRequestID = shared.ClientRequestID
	reuse.Values = values("species", `"Kefal"`)
	if got, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), reuse); err != nil || got.ID != first.ID {
		t.Fatalf("aynı istek kimliği: %+v %v", got, err)
	}

	// Who sees which location: the author all; participants (going) and
	// managers the participants' and the tayfa's; other members the
	// tayfa's only.
	for name, tc := range map[string]struct {
		user id.ID
		sees []id.ID
	}{
		"sahibi":      {angler, []id.ID{first.ID, private.ID, public.ID}},
		"katılımcı":   {partner, []id.ID{first.ID, public.ID}},
		"moderatör":   {moderator, []id.ID{first.ID, public.ID}},
		"organizatör": {admin, []id.ID{first.ID, public.ID}},
		"katılmayan":  {outsider, []id.ID{public.ID}},
	} {
		page, err := e.logs.List(ctx, ref(tc.user, tf, ev.ID, key), 0, "")
		must(t, err)
		got := byID(page)
		if len(got) != 4 {
			t.Fatalf("%s: %d kayıt", name, len(got))
		}
		for eid, x := range got {
			want := false
			for _, s := range tc.sees {
				want = want || s == eid
			}
			if (x.Location != nil) != want {
				t.Errorf("%s: %s konumu %v", name, x.Values["species"], x.Location != nil)
			}
			// Only the author learns the visibility.
			if (x.LocationVisibility != "") != (tc.user == angler && x.ID != noSpot.ID) {
				t.Errorf("%s: görünürlük %q", name, x.LocationVisibility)
			}
			if x.PhotoUploadID != (id.ID{}) != (x.ID == noSpot.ID) {
				t.Errorf("%s: fotoğraf %v", name, x.PhotoUploadID)
			}
		}
	}

	// Neither the values nor the location reach the audit record or the
	// outbox.
	var meta, payload string
	must(t, e.pool.QueryRow(ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_logs WHERE action = $1`, logbook.AuditAdded).Scan(&meta))
	must(t, e.pool.QueryRow(ctx, `SELECT string_agg(payload::text, ' ') FROM domain_events WHERE subject_id = $1`, ev.ID.PG()).Scan(&payload))
	for _, s := range []string{meta, payload} {
		for _, secret := range []string{"38.35", "26.77", "Levrek", "Çipura", "1.26"} {
			if strings.Contains(s, secret) {
				t.Errorf("kayıtta konum/içerik: %s", s)
			}
		}
	}
	if !strings.Contains(meta, first.ID.String()) {
		t.Errorf("denetim kaydı kaydı adlandırmıyor: %s", meta)
	}

	// Removal: someone else's only by moderators; one's own by the author.
	wantErr(t, "başkasının kaydı", e.logs.Remove(ctx, ref(partner, tf, ev.ID, key), first.ID), apperr.ErrPermissionDenied)
	must(t, e.logs.Remove(ctx, ref(angler, tf, ev.ID, key), private.ID))
	wantErr(t, "silinmiş kayıt", e.logs.Remove(ctx, ref(angler, tf, ev.ID, key), private.ID), apperr.ErrEventItemNotFound)
	wantErr(t, "olmayan kayıt", e.logs.Remove(ctx, ref(moderator, tf, ev.ID, key), id.New()), apperr.ErrEventItemNotFound)
	must(t, e.logs.Remove(ctx, ref(moderator, tf, ev.ID, key), first.ID))
	page, err = e.logs.List(ctx, ref(angler, tf, ev.ID, key), 0, "")
	must(t, err)
	if got := byID(page); len(got) != 2 || got[public.ID].ID != public.ID || got[noSpot.ID].ID != noSpot.ID {
		t.Fatalf("silinenler listede: %+v", page.Entries)
	}
	// A removed entry's client request ID is not reused.
	_, err = e.logs.Add(ctx, ref(angler, tf, ev.ID, key), shared)
	wantField(t, "silinmiş kaydın istek kimliği", err, "client_request_id")

	// The photo goes when the entry is deleted for good (the 30-day purge,
	// core_app, triggers on).
	_, err = e.pool.Exec(ctx, "DELETE FROM event_log_entries WHERE id = $1", noSpot.ID.PG())
	must(t, err)
	if s := mediatest.Status(t, e.pool, photo); s != "deleting" {
		t.Fatalf("kayıt silinince fotoğraf silinmeli: %s", s)
	}
}

// A photo is the author's own READY log photo of the tayfa.
func TestLogPhoto(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeFishing)
	angler := e.attended(t, tf, ev.ID, authz.Member)
	other := e.member(t, tf, authz.Member)
	add := func(photo id.ID) error {
		in := catch("Levrek", nil, "")
		in.PhotoUploadID = photo
		_, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, "catches"), in)
		return err
	}
	wantErr(t, "başkasının fotoğrafı", add(mediatest.Ready(t, e.pool, other, media.PurposeLogPhoto, tf)), apperr.ErrUploadNotFound)
	wantErr(t, "kapak günlük fotoğrafı olamaz", add(mediatest.Ready(t, e.pool, angler, media.PurposeCover, tf)), apperr.ErrUploadNotFound)
	tf2, _ := e.newTayfa(t)
	wantErr(t, "başka tayfanın fotoğrafı", add(mediatest.Ready(t, e.pool, angler, media.PurposeLogPhoto, tf2)), apperr.ErrUploadNotFound)
	photo := mediatest.Ready(t, e.pool, angler, media.PurposeLogPhoto, tf)
	must(t, add(photo))
	wantErr(t, "aynı fotoğraf iki kayıtta", add(photo), apperr.ErrUploadInUse)
	if n := e.count(t, `SELECT count(*) FROM event_log_entries`); n != 1 {
		t.Fatalf("reddedilen kayıtlar yazıldı: %d", n)
	}
}

// The write window (ADR-0025 §5): entries freeze seven days after the end
// and a cancelled event takes none; a moderator still removes them. The
// per-person bound, the fields of another log and a location on a log
// without one.
func TestLogWriteWindowAndBounds(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, booksType)
	const key = "books"
	reader := e.attended(t, tf, ev.ID, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	book := func(title string) logbook.Input {
		return logbook.Input{Values: values("book", `"`+title+`"`, "pages", `120`), ClientRequestID: id.New().String()}
	}

	page, err := e.logs.List(ctx, ref(reader, tf, ev.ID, key), 0, "")
	must(t, err)
	if page.LocationEnabled || page.LocationDefault != "" || page.MaxEntries != 2 || !page.CallerCanAdd {
		t.Fatalf("kitap günlüğü: %+v", page)
	}
	_, err = e.logs.Add(ctx, ref(reader, tf, ev.ID, key), logbook.Input{Values: values("book", `"Tutunamayanlar"`), Location: &logbook.Point{Lat: 41, Lng: 29}, ClientRequestID: id.New().String()})
	wantField(t, "konumsuz günlükte konum", err, "location")
	_, err = e.logs.Add(ctx, ref(reader, tf, ev.ID, key), logbook.Input{Values: values("species", `"Levrek"`), ClientRequestID: id.New().String()})
	wantField(t, "başka günlüğün alanı", err, "values.book")

	first, err := e.logs.Add(ctx, ref(reader, tf, ev.ID, key), book("Tutunamayanlar"))
	must(t, err)
	_, err = e.logs.Add(ctx, ref(reader, tf, ev.ID, key), book("Saatleri Ayarlama Enstitüsü"))
	must(t, err)
	_, err = e.logs.Add(ctx, ref(reader, tf, ev.ID, key), book("Kürk Mantolu Madonna"))
	wantErr(t, "kişi başı sınır", err, apperr.ErrEventLimit)

	// Seven days after the end the log freezes: no adding, no removing
	// one's own; a moderator still removes.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.logs.Add(ctx, ref(reader, tf, ev.ID, key), book("Kürk Mantolu Madonna"))
	wantErr(t, "donmuş günlüğe ekleme", err, apperr.ErrComponentFrozen)
	wantErr(t, "donmuş günlükten kendi kaydını silme", e.logs.Remove(ctx, ref(reader, tf, ev.ID, key), first.ID), apperr.ErrComponentFrozen)
	page, err = e.logs.List(ctx, ref(reader, tf, ev.ID, key), 0, "")
	must(t, err)
	if page.CallerCanAdd || byID(page)[first.ID].CallerCanRemove {
		t.Fatalf("donmuş günlükte yazma izni: %+v", page)
	}
	if page, err := e.logs.List(ctx, ref(moderator, tf, ev.ID, key), 0, ""); err != nil || !byID(page)[first.ID].CallerCanRemove {
		t.Fatalf("moderatör donmuş kaydı silemiyor görünüyor: %+v %v", page, err)
	}
	must(t, e.logs.Remove(ctx, ref(moderator, tf, ev.ID, key), first.ID))

	// A cancelled event takes no entry.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, booksType)
	reader2 := e.attended(t, tf, cancelled.ID, authz.Member)
	kept, err := e.logs.Add(ctx, ref(reader2, tf, cancelled.ID, key), book("İnce Memed"))
	must(t, err)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.logs.Add(ctx, ref(reader2, tf, cancelled.ID, key), book("Yaban"))
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	must(t, e.logs.Remove(ctx, ref(moderator, tf, cancelled.ID, key), kept.ID))
}

// Who may do what, for every role (ADR-0025 §5); IDOR.
func TestLogPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeFishing)
	const key = "catches"
	member := e.attended(t, tf, ev.ID, authz.Member)
	moderator := e.attended(t, tf, ev.ID, authz.Moderator)
	muted := e.attended(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, muted)
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"üye", member, nil},
		{"moderatör", moderator, nil},
		{"susturulmuş üye", muted, apperr.ErrMuted},
		{"üye değil", id.New(), apperr.ErrTayfaNotFound},
	} {
		_, err := e.logs.Add(ctx, ref(tc.user, tf, ev.ID, key), catch("Levrek", nil, ""))
		if tc.want == nil {
			must(t, err)
		} else {
			wantErr(t, tc.name, err, tc.want)
		}
		_, err = e.logs.List(ctx, ref(tc.user, tf, ev.ID, key), 0, "")
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
		} else {
			// A mute does not hide the log.
			must(t, err)
		}
	}
	page, err := e.logs.List(ctx, ref(muted, tf, ev.ID, key), 0, "")
	must(t, err)
	if page.CallerCanAdd {
		t.Fatal("susturulmuş üye ekleyebilir görünüyor")
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.logs.List(ctx, ref(admin2, tf2, ev.ID, key), 0, "")
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.logs.Add(ctx, ref(admin2, tf2, ev.ID, key), catch("Levrek", nil, ""))
	wantErr(t, "başka tayfa ekleme", err, apperr.ErrEventNotFound)
	wantErr(t, "başka tayfa silme", e.logs.Remove(ctx, ref(admin2, tf2, ev.ID, key), id.New()), apperr.ErrEventNotFound)
	// An entry of another event is not found through this one.
	other := e.newEvent(t, tf, admin, event.TypeFishing)
	page, err = e.logs.List(ctx, ref(member, tf, ev.ID, key), 0, "")
	must(t, err)
	wantErr(t, "başka etkinliğin kaydı", e.logs.Remove(ctx, ref(moderator, tf, other.ID, key), page.Entries[0].ID), apperr.ErrEventItemNotFound)
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on. The
// default visibility comes from the definition.
func TestLogOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	fishing := e.newEvent(t, tf, admin, event.TypeFishing)
	off := e.newEvent(t, tf, admin, optType)
	birds := e.newEvent(t, tf, admin, optType, component.Selection{Key: "sightings", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":      ref(admin, tf, general.ID, "catches"),
		"anahtar yerine kind": ref(admin, tf, fishing.ID, "log"),
		"başka bileşen":       ref(admin, tf, fishing.ID, "meeting_point"),
		"başka türün günlüğü": ref(admin, tf, fishing.ID, "books"),
	} {
		_, err := e.logs.List(ctx, r, 0, "")
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.logs.Add(ctx, r, catch("Levrek", nil, ""))
		wantErr(t, name+" ekleme", err, apperr.ErrComponentNotFound)
	}
	_, err := e.logs.List(ctx, ref(admin, tf, off.ID, "sightings"), 0, "")
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.logs.Add(ctx, ref(admin, tf, off.ID, "sightings"), catch("Kırlangıç", nil, ""))
	wantErr(t, "kapalı bileşene ekleme", err, apperr.ErrComponentDisabled)

	watcher := e.attended(t, tf, birds.ID, authz.Member)
	got, err := e.logs.Add(ctx, ref(watcher, tf, birds.ID, "sightings"), catch("Kırlangıç", &logbook.Point{Lat: 40.1, Lng: 26.4}, ""))
	must(t, err)
	if got.LocationVisibility != logbook.LocationParticipants {
		t.Fatalf("tanımdaki varsayılan gizlilik: %+v", got)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_log_entries (id, event_id, component_key, user_id, client_request_id)
			VALUES (gen_random_uuid(), $1, 'sightings', gen_random_uuid(), gen_random_uuid())`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

// Pages are oldest first; a token works only for its own log.
func TestLogPaging(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeFishing)
	const key = "catches"
	angler := e.attended(t, tf, ev.ID, authz.Member)
	var want []id.ID
	for i := range 3 {
		e.clock.set(e.clock.now().Add(time.Duration(i+1) * time.Second))
		x, err := e.logs.Add(ctx, ref(angler, tf, ev.ID, key), catch("Levrek", nil, ""))
		must(t, err)
		want = append(want, x.ID)
	}
	var got []id.ID
	token := ""
	for range 4 {
		page, err := e.logs.List(ctx, ref(angler, tf, ev.ID, key), 2, token)
		must(t, err)
		for _, x := range page.Entries {
			got = append(got, x.ID)
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("sıra = %v, %v bekleniyordu", got, want)
	}
	page, err := e.logs.List(ctx, ref(angler, tf, ev.ID, key), 1, "")
	must(t, err)
	other := e.newEvent(t, tf, admin, event.TypeFishing)
	_, err = e.logs.List(ctx, ref(angler, tf, other.ID, key), 1, page.NextPageToken)
	wantField(t, "başka günlüğün sayfası", err, "page_token")
	_, err = e.logs.List(ctx, ref(angler, tf, ev.ID, key), 1, "bozuk")
	wantField(t, "bozuk sayfa", err, "page_token")
	_, err = e.logs.List(ctx, ref(angler, tf, ev.ID, key), -1, "")
	wantField(t, "eksi sayfa", err, "page_size")
}
