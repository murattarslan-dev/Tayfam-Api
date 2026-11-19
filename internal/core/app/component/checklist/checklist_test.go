package checklist_test

import (
	"context"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/checklist"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. The catalog's camp has the "gear" list with assignment and
// quantity. listsType has two lists, "gear" like the camp's and "duty"
// without assignment or quantity, next to another component; its second
// version, written by a test, has no lists. optType has an optional list
// that is off by default.
const (
	listsType = "test_lists"
	listsV1   = `{"components":[{"kind":"checklist","key":"gear","params":{"label":"gear","assignment":true,"quantity":true}},{"kind":"checklist","key":"duty","params":{"label":"duty"}},{"kind":"draw"}]}`
	listsV2   = `{"components":[{"kind":"draw"}]}`
	optType   = "test_opt_list"
	optV1     = `{"components":[{"kind":"checklist","optional":{"default":false},"params":{"assignment":true}}]}`
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
	fw     *component.Service
	lists  *checklist.Service
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
	e := &env{fw: fw, lists: checklist.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, listsType, listsV1)
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

// newTayfa creates a tayfa (every module on) and returns it with its
// admin.
func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Kamp Tayfası", ClientRequestID: id.New().String()})
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

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Hafta sonu kampı", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(),
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

// going answers "going" for every user.
func (e *env) going(t *testing.T, tf, ev id.ID, users ...id.ID) {
	t.Helper()
	for _, u := range users {
		_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
		must(t, err)
	}
}

// letMembersCreate lets plain members create events, so that an organizer
// who is not a moderator exists.
func (e *env) letMembersCreate(t *testing.T, tf, admin id.ID) {
	t.Helper()
	_, err := e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
}

// named returns the item called name in l.
func named(t *testing.T, l checklist.List, name string) checklist.Item {
	t.Helper()
	for _, it := range l.Items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("%q maddesi yok: %+v", name, l.Items)
	return checklist.Item{}
}

// The camp's list end to end (the catalog's camp, key "gear"): add, list,
// remove; the bound; nothing of the item's name in the audit record or the
// outbox.
func TestChecklistAddListRemove(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, event.TypeCamp)
	const key = "gear"
	e.going(t, tf, ev.ID, member)

	// Nothing yet: an empty list, not an error.
	got, err := e.lists.List(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if got.Items == nil || len(got.Items) != 0 || got.Label != "gear" || !got.AssignmentEnabled || !got.QuantityEnabled ||
		got.CallerCanManage || !got.CallerCanTake {
		t.Fatalf("boş liste: %+v", got)
	}
	if got, err := e.lists.List(ctx, ref(admin, tf, ev.ID, key)); err != nil || !got.CallerCanManage || got.CallerCanTake {
		t.Fatalf("yöneticinin okuması: %+v %v", got, err)
	}

	// Members do not add.
	_, err = e.lists.Add(ctx, ref(member, tf, ev.ID, key), checklist.Input{Name: "Çadır"})
	wantErr(t, "üyenin eklemesi", err, apperr.ErrPermissionDenied)

	for _, tc := range []struct {
		name  string
		in    checklist.Input
		field string
	}{
		{"ad yok", checklist.Input{}, "name"},
		{"boşluk ad", checklist.Input{Name: "   "}, "name"},
		{"uzun ad", checklist.Input{Name: strings.Repeat("ç", checklist.NameMax+1)}, "name"},
		{"denetim karakterli ad", checklist.Input{Name: "Ça\x00dır"}, "name"},
		{"eksi adet", checklist.Input{Name: "Çadır", Quantity: -1}, "quantity"},
		{"1000 adet", checklist.Input{Name: "Çadır", Quantity: checklist.QuantityMax + 1}, "quantity"},
	} {
		_, err := e.lists.Add(ctx, ref(admin, tf, ev.ID, key), tc.in)
		wantField(t, tc.name, err, tc.field)
	}
	// The assignee must be an active member who answered going.
	_, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, key), checklist.Input{Name: "Çadır", AssignedUserID: id.New()})
	wantErr(t, "üye olmayana atama", err, apperr.ErrEventNotGoing)
	_, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, key), checklist.Input{Name: "Çadır", AssignedUserID: admin})
	wantErr(t, "katılmayana atama", err, apperr.ErrEventNotGoing)
	if n := e.count(t, `SELECT count(*) FROM event_checklist_items`); n != 0 {
		t.Fatalf("reddedilen madde yazıldı: %d", n)
	}

	added, err := e.lists.Add(ctx, ref(admin, tf, ev.ID, key), checklist.Input{Name: " Çadır ", Quantity: 2, AssignedUserID: member})
	must(t, err)
	tent := named(t, added, "Çadır")
	if len(added.Items) != 1 || tent.Quantity != 2 || tent.AssignedUserID != member {
		t.Fatalf("eklenen madde: %+v", added)
	}
	added, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, key), checklist.Input{Name: "Ocak"})
	must(t, err)
	if stove := named(t, added, "Ocak"); stove.Quantity != 1 || stove.AssignedUserID != (id.ID{}) || added.Items[0].ID != tent.ID {
		t.Fatalf("ikinci madde: %+v", added)
	}
	if n := e.changes(t, ev.ID, key); n != 2 {
		t.Fatalf("canlı güncelleme olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND actor_id = $3 AND metadata ->> 'component_key' = $4 AND metadata ->> 'item_id' = $5`,
		checklist.AuditAdded, ev.ID.PG(), admin.PG(), key, tent.ID.String()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	// The item's name is content: never in the audit record or an event.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Çadır%'`) +
		e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Çadır%'`); n != 0 {
		t.Fatalf("madde adı kayda sızdı: %d", n)
	}

	// Remove: managers only; an unknown item is not found.
	_, err = e.lists.Remove(ctx, ref(member, tf, ev.ID, key), tent.ID)
	wantErr(t, "üyenin çıkarması", err, apperr.ErrPermissionDenied)
	_, err = e.lists.Remove(ctx, ref(admin, tf, ev.ID, key), id.New())
	wantErr(t, "olmayan madde", err, apperr.ErrEventItemNotFound)
	left, err := e.lists.Remove(ctx, ref(admin, tf, ev.ID, key), tent.ID)
	must(t, err)
	if len(left.Items) != 1 || left.Items[0].Name != "Ocak" {
		t.Fatalf("çıkarma sonrası: %+v", left)
	}
	_, err = e.lists.Remove(ctx, ref(admin, tf, ev.ID, key), tent.ID)
	wantErr(t, "ikinci çıkarma", err, apperr.ErrEventItemNotFound)

	// A list holds at most MaxItems.
	e.exec(t, `INSERT INTO event_checklist_items (id, event_id, component_key, name)
		SELECT gen_random_uuid(), $1, $2, 'Madde ' || g FROM generate_series(2, $3::int) AS g`, ev.ID.PG(), key, checklist.MaxItems)
	_, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, key), checklist.Input{Name: "Fazla"})
	wantErr(t, "dolu liste", err, apperr.ErrEventLimit)
}

// Two lists on one event are separate, and a list's parameters decide what
// it takes.
func TestChecklistKeysAndParams(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, listsType)
	e.going(t, tf, ev.ID, member)

	gear, err := e.lists.Add(ctx, ref(admin, tf, ev.ID, "gear"), checklist.Input{Name: "Çadır", Quantity: 3})
	must(t, err)
	duty, err := e.lists.Add(ctx, ref(admin, tf, ev.ID, "duty"), checklist.Input{Name: "Gece nöbeti"})
	must(t, err)
	if len(gear.Items) != 1 || len(duty.Items) != 1 || duty.Label != "duty" || duty.AssignmentEnabled || duty.QuantityEnabled || duty.CallerCanTake {
		t.Fatalf("iki liste: %+v / %+v", gear, duty)
	}
	// An item of one list is not an item of the other.
	_, err = e.lists.Remove(ctx, ref(admin, tf, ev.ID, "duty"), gear.Items[0].ID)
	wantErr(t, "başka listenin maddesi", err, apperr.ErrEventItemNotFound)
	_, err = e.lists.Assign(ctx, ref(admin, tf, ev.ID, "gear"), duty.Items[0].ID, member)
	wantErr(t, "başka listenin maddesine atama", err, apperr.ErrEventItemNotFound)

	// Without the parameters: no quantity above one, no assignee.
	_, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, "duty"), checklist.Input{Name: "Odun", Quantity: 2})
	wantField(t, "adet kapalıyken adet", err, "quantity")
	_, err = e.lists.Add(ctx, ref(admin, tf, ev.ID, "duty"), checklist.Input{Name: "Odun", AssignedUserID: member})
	wantField(t, "atama kapalıyken atanan", err, "assigned_user_id")
	_, err = e.lists.Assign(ctx, ref(admin, tf, ev.ID, "duty"), duty.Items[0].ID, member)
	wantField(t, "atama kapalıyken atama", err, "assigned_user_id")
	_, err = e.lists.Assign(ctx, ref(member, tf, ev.ID, "duty"), duty.Items[0].ID, member)
	wantField(t, "atama kapalıyken üstlenme", err, "assigned_user_id")
	if _, err := e.lists.Add(ctx, ref(admin, tf, ev.ID, "duty"), checklist.Input{Name: "Odun", Quantity: 1}); err != nil {
		t.Fatalf("adet kapalıyken bir adet: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM event_checklist_items WHERE event_id = $1`, ev.ID.PG()); n != 3 {
		t.Fatalf("madde sayısı = %d", n)
	}
}

// TestChecklistAssign: who may take, drop and assign an item (the Faz 1
// camp rule, issue #198).
func TestChecklistAssign(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.letMembersCreate(t, tf, admin)
	org := e.member(t, tf, authz.Member)
	a, b, none := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, org, event.TypeCamp)
	const key = "gear"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	e.going(t, tf, ev.ID, a, b)
	_, err := e.events.SetRSVP(ctx, none, tf, ev.ID, event.RSVPMaybe)
	must(t, err)

	newItem := func(assigned id.ID) id.ID {
		t.Helper()
		l, err := e.lists.Add(ctx, r(org), checklist.Input{Name: "Ocak " + id.New().String()[:8], AssignedUserID: assigned})
		must(t, err)
		return l.Items[len(l.Items)-1].ID
	}
	assignee := func(item id.ID) id.ID {
		t.Helper()
		l, err := e.lists.List(ctx, r(org))
		must(t, err)
		for _, it := range l.Items {
			if it.ID == item {
				return it.AssignedUserID
			}
		}
		t.Fatalf("madde yok: %s", item)
		return id.ID{}
	}

	for _, tc := range []struct {
		name         string
		caller       id.ID
		from, to     id.ID
		want         error
		wantAssignee id.ID
	}{
		{"üye boştakini üstlenir", a, id.ID{}, a, nil, a},
		{"üye kendi maddesini bırakır", a, a, id.ID{}, nil, id.ID{}},
		{"üye başkasına atayamaz", a, id.ID{}, b, apperr.ErrPermissionDenied, id.ID{}},
		{"üye başkasınınkini alamaz", a, b, a, apperr.ErrEventItemTaken, b},
		{"üye başkasınınkini bırakamaz", a, b, id.ID{}, apperr.ErrEventItemTaken, b},
		{"katılmayan üstlenemez", none, id.ID{}, none, apperr.ErrEventNotGoing, id.ID{}},
		{"organizatör katılana atar", org, id.ID{}, b, nil, b},
		{"organizatör katılmayana atayamaz", org, id.ID{}, none, apperr.ErrEventNotGoing, id.ID{}},
		{"organizatör başkasınınkini değiştirir", org, a, b, nil, b},
		{"organizatör atamayı kaldırır", org, a, id.ID{}, nil, id.ID{}},
		{"yönetici atar", admin, id.ID{}, a, nil, a},
		{"aynı değer değişiklik değil", a, a, a, nil, a},
	} {
		item := newItem(tc.from)
		_, err := e.lists.Assign(ctx, r(tc.caller), item, tc.to)
		wantErr(t, tc.name, err, tc.want)
		if got := assignee(item); got != tc.wantAssignee {
			t.Errorf("%s: atanan %s, %s bekleniyordu", tc.name, got, tc.wantAssignee)
		}
	}
	_, err = e.lists.Assign(ctx, r(a), id.New(), a)
	wantErr(t, "olmayan madde", err, apperr.ErrEventItemNotFound)

	// Of members taking one item at once, one wins and the others see it
	// taken.
	item := newItem(id.ID{})
	users := []id.ID{a, b}
	for range 3 {
		u := e.member(t, tf, authz.Member)
		e.going(t, tf, ev.ID, u)
		users = append(users, u)
	}
	errs := make([]error, len(users))
	var wg sync.WaitGroup
	for i, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.lists.Assign(ctx, r(u), item, u)
		}()
	}
	wg.Wait()
	winner := id.ID{}
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != (id.ID{}) {
				t.Fatal("iki kişi aynı maddeyi aldı")
			}
			winner = users[i]
		case !errors.Is(err, apperr.ErrEventItemTaken):
			t.Errorf("yarış: %v", err)
		}
	}
	if winner == (id.ID{}) || assignee(item) != winner {
		t.Fatalf("kazanan = %s, kayıt = %s", winner, assignee(item))
	}
}

// Every role against every action (ADR-0004: default no).
func TestChecklistPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.letMembersCreate(t, tf, admin)
	org, moderator := e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator)
	member, mutedMember, mutedModerator := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator)
	stranger := id.New()
	ev := e.newEvent(t, tf, org, event.TypeCamp)
	const key = "gear"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	e.going(t, tf, ev.ID, member, mutedMember, mutedModerator)
	for _, u := range []id.ID{mutedMember, mutedModerator} {
		e.mute(t, tf, u)
	}

	denied, muted, gone := apperr.ErrPermissionDenied, apperr.ErrMuted, apperr.ErrTayfaNotFound
	for _, row := range []struct {
		who                string
		user               id.ID
		read, add, take    error
		canManage, canTake bool
	}{
		{"yönetici (katılmıyor)", admin, nil, nil, nil, true, false},
		{"moderatör (katılmıyor)", moderator, nil, nil, nil, true, false},
		{"organizatör (üye, katılmıyor)", org, nil, nil, nil, true, false},
		{"üye (katılıyor)", member, nil, denied, nil, false, true},
		{"susturulmuş üye (katılıyor)", mutedMember, nil, denied, muted, false, false},
		{"susturulmuş moderatör (katılıyor)", mutedModerator, nil, muted, muted, false, false},
		{"üye değil", stranger, gone, gone, gone, false, false},
	} {
		// Something on the list for everyone to read and take.
		seed, err := e.lists.Add(ctx, r(admin), checklist.Input{Name: "Madde " + id.New().String()[:8]})
		must(t, err)
		item := seed.Items[len(seed.Items)-1].ID

		got, err := e.lists.List(ctx, r(row.user))
		wantErr(t, row.who+" / okuma", err, row.read)
		if row.read == nil && (len(got.Items) == 0 || got.CallerCanManage != row.canManage || got.CallerCanTake != row.canTake) {
			t.Errorf("%s / okunan: %d madde, yönetebilir=%v, üstlenebilir=%v", row.who, len(got.Items), got.CallerCanManage, got.CallerCanTake)
		}
		_, err = e.lists.Add(ctx, r(row.user), checklist.Input{Name: "Ekleme"})
		wantErr(t, row.who+" / ekleme", err, row.add)
		// Managers take an item for a going member; the rest for
		// themselves.
		to := row.user
		if row.canManage {
			to = member
		}
		_, err = e.lists.Assign(ctx, r(row.user), item, to)
		wantErr(t, row.who+" / atama", err, row.take)
		_, err = e.lists.Remove(ctx, r(row.user), item)
		wantErr(t, row.who+" / çıkarma", err, row.add)
	}

	// IDOR: an event of another tayfa under the caller's own tayfa, and an
	// item of another event under this one.
	otherTayfa, otherAdmin := e.newTayfa(t)
	foreign := e.newEvent(t, otherTayfa, otherAdmin, event.TypeCamp)
	fl, err := e.lists.Add(ctx, ref(otherAdmin, otherTayfa, foreign.ID, key), checklist.Input{Name: "Yabancı"})
	must(t, err)
	foreignItem := fl.Items[0].ID
	_, err = e.lists.List(ctx, ref(admin, tf, foreign.ID, key))
	wantErr(t, "başka tayfanın etkinliği / okuma", err, apperr.ErrEventNotFound)
	_, err = e.lists.Add(ctx, ref(admin, tf, foreign.ID, key), checklist.Input{Name: "Sızma"})
	wantErr(t, "başka tayfanın etkinliği / ekleme", err, apperr.ErrEventNotFound)
	_, err = e.lists.Remove(ctx, ref(admin, otherTayfa, foreign.ID, key), foreignItem)
	wantErr(t, "başka tayfanın yöneticisi / çıkarma", err, gone)
	_, err = e.lists.Remove(ctx, r(admin), foreignItem)
	wantErr(t, "başka etkinliğin maddesi / çıkarma", err, apperr.ErrEventItemNotFound)
	_, err = e.lists.Assign(ctx, r(member), foreignItem, member)
	wantErr(t, "başka etkinliğin maddesi / üstlenme", err, apperr.ErrEventItemNotFound)
	if n := e.count(t, `SELECT count(*) FROM event_checklist_items WHERE event_id = $1 AND assigned_user_id IS NULL`, foreign.ID.PG()); n != 1 {
		t.Fatalf("reddedilen istek başka tayfanın maddesine dokundu: %d", n)
	}
}

// The write window: the list changes until the event ends (the Faz 1
// rule), within the framework's window; never on a cancelled event or in a
// read-only tayfa. Reading always works.
func TestChecklistWriteWindow(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, event.TypeCamp)
	cancelled := e.newEvent(t, tf, admin, event.TypeCamp)
	open := e.newEvent(t, tf, admin, event.TypeCamp)
	const key = "gear"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	for _, x := range []id.ID{ev.ID, cancelled.ID, open.ID} {
		e.going(t, tf, x, member)
	}
	l, err := e.lists.Add(ctx, r(admin), checklist.Input{Name: "Çadır"})
	must(t, err)
	item := l.Items[0].ID

	// Started: everything still works.
	e.clock.set(ev.StartsAt.Add(time.Minute))
	_, err = e.lists.Add(ctx, r(admin), checklist.Input{Name: "Ocak"})
	must(t, err)
	_, err = e.lists.Assign(ctx, r(member), item, member)
	must(t, err)

	// Ended: nothing changes any more.
	for _, at := range []time.Time{ev.EndsAt, ev.EndsAt.Add(component.FreezeAfter - time.Microsecond)} {
		e.clock.set(at)
		_, err = e.lists.Add(ctx, r(admin), checklist.Input{Name: "Su"})
		wantErr(t, "bitmiş etkinlikte ekleme", err, apperr.ErrEventEnded)
		_, err = e.lists.Assign(ctx, r(member), item, id.ID{})
		wantErr(t, "bitmiş etkinlikte bırakma", err, apperr.ErrEventEnded)
		_, err = e.lists.Remove(ctx, r(admin), item)
		wantErr(t, "bitmiş etkinlikte çıkarma", err, apperr.ErrEventEnded)
		got, err := e.lists.List(ctx, r(admin))
		must(t, err)
		if len(got.Items) != 2 || got.CallerCanManage || got.CallerCanTake {
			t.Fatalf("bitmiş etkinlikte okuma: %+v", got)
		}
	}
	// Frozen: seven days after the end.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.lists.Remove(ctx, r(admin), item)
	wantErr(t, "donmuş etkinlikte çıkarma", err, apperr.ErrComponentFrozen)
	_, err = e.lists.Assign(ctx, r(member), item, id.ID{})
	wantErr(t, "donmuş etkinlikte bırakma", err, apperr.ErrComponentFrozen)

	// A cancelled event.
	e.clock.set(ev.StartsAt.Add(-time.Hour))
	cr := func(u id.ID) component.Ref { return ref(u, tf, cancelled.ID, key) }
	cl, err := e.lists.Add(ctx, cr(admin), checklist.Input{Name: "Çadır"})
	must(t, err)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.lists.Add(ctx, cr(admin), checklist.Input{Name: "Ocak"})
	wantErr(t, "iptal edilmiş etkinlikte ekleme", err, apperr.ErrEventCancelled)
	_, err = e.lists.Assign(ctx, cr(member), cl.Items[0].ID, member)
	wantErr(t, "iptal edilmiş etkinlikte üstlenme", err, apperr.ErrEventCancelled)
	if got, err := e.lists.List(ctx, cr(member)); err != nil || len(got.Items) != 1 || got.CallerCanTake || got.CallerCanManage {
		t.Fatalf("iptal edilmiş etkinlikte okuma: %+v %v", got, err)
	}

	// A read-only tayfa.
	or := func(u id.ID) component.Ref { return ref(u, tf, open.ID, key) }
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	_, err = e.lists.Add(ctx, or(admin), checklist.Input{Name: "Çadır"})
	wantErr(t, "salt okunur tayfada ekleme", err, apperr.ErrTayfaReadOnly)
	if got, err := e.lists.List(ctx, or(admin)); err != nil || got.CallerCanManage || got.CallerCanTake {
		t.Fatalf("salt okunur tayfada okuma: %+v %v", got, err)
	}
}

// Items are written only where the event's type version has the list
// (ADR-0025 §4): the service refuses, and so does the table's trigger for a
// write that goes around the service.
func TestChecklistOnlyWhereTheTypeVersionHasIt(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := checklist.Input{Name: "Çadır"}
	refused := func(what string, ev id.ID, key string) {
		t.Helper()
		_, err := e.pool.Exec(ctx, `INSERT INTO event_checklist_items (id, event_id, component_key, name) VALUES ($1, $2, $3, 'Çadır')`,
			id.New().PG(), ev.PG(), key)
		if constraintOf(err) != "event_component_guard" {
			t.Errorf("%s: doğrudan yazma: %v, event_component_guard bekleniyordu", what, err)
		}
	}
	all := func(what string, r component.Ref, want error) {
		t.Helper()
		_, err := e.lists.List(ctx, r)
		wantErr(t, what+" / okuma", err, want)
		_, err = e.lists.Add(ctx, r, in)
		wantErr(t, what+" / ekleme", err, want)
		_, err = e.lists.Remove(ctx, r, id.New())
		wantErr(t, what+" / çıkarma", err, want)
		_, err = e.lists.Assign(ctx, r, id.New(), id.ID{})
		wantErr(t, what+" / atama", err, want)
	}

	// A type without the component.
	plain := e.newEvent(t, tf, admin, event.TypeGeneral)
	all("bileşensiz tür", ref(admin, tf, plain.ID, "checklist"), apperr.ErrComponentNotFound)
	refused("bileşensiz tür", plain.ID, "checklist")

	// Keyed lists: the key, not the kind; a component of another kind is
	// not a list.
	lists := e.newEvent(t, tf, admin, listsType)
	_, err := e.lists.Add(ctx, ref(admin, tf, lists.ID, "gear"), in)
	must(t, err)
	all("anahtarlı bileşende kind", ref(admin, tf, lists.ID, "checklist"), apperr.ErrComponentNotFound)
	all("başka türün bileşeni", ref(admin, tf, lists.ID, "draw"), apperr.ErrComponentNotFound)
	refused("anahtarlı bileşende kind", lists.ID, "checklist")
	refused("başka türün bileşeni", lists.ID, "draw")

	// The type's next version drops the lists: the old event keeps them,
	// an event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, listsType, listsV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, listsType, listsV2)
	newer := e.newEvent(t, tf, admin, listsType)
	if newer.TypeVersion != 2 || lists.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", lists.TypeVersion, newer.TypeVersion)
	}
	all("yeni sürümde olmayan bileşen", ref(admin, tf, newer.ID, "gear"), apperr.ErrComponentNotFound)
	refused("yeni sürümde olmayan bileşen", newer.ID, "gear")
	if got, err := e.lists.Add(ctx, ref(admin, tf, lists.ID, "gear"), checklist.Input{Name: "Ocak"}); err != nil || len(got.Items) != 2 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// An optional list that is off: every method answers
	// component-disabled until the organizer switches it on. It sets no
	// label.
	opt := e.newEvent(t, tf, admin, optType)
	all("kapalı bileşen", ref(admin, tf, opt.ID, "checklist"), apperr.ErrComponentDisabled)
	refused("kapalı bileşen", opt.ID, "checklist")
	_, err = e.fw.Set(ctx, admin, tf, opt.ID, component.Selection{Key: "checklist", Enabled: on(true)})
	must(t, err)
	got, err := e.lists.Add(ctx, ref(admin, tf, opt.ID, "checklist"), in)
	if err != nil || got.Label != checklist.DefaultLabel || !got.AssignmentEnabled || got.QuantityEnabled || len(got.Items) != 1 {
		t.Fatalf("açılan bileşen: %+v %v", got, err)
	}

	if n := e.count(t, `SELECT count(*) FROM event_checklist_items`); n != 3 {
		t.Fatalf("madde satırları = %d (listeler ve açılan liste)", n)
	}

	// The events module off: nothing is reachable, reads included.
	must(t, e.tayfas.SetModule(ctx, admin, tf, "events", false))
	all("modül kapalı", ref(admin, tf, lists.ID, "gear"), apperr.ErrModuleDisabled)
}
