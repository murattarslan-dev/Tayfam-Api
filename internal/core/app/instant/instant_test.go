package instant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }

type env struct {
	pool    *pgxpool.Pool
	admin   *pgxpool.Pool
	svc     *Service
	tayfas  *tayfa.Service
	events  *event.Service
	members *membership.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tf, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	ev, err := event.New(event.Config{Pool: d.App, Log: log})
	must(t, err)
	ms, err := membership.New(membership.Config{Pool: d.App, JoinLimiter: allowAll{}})
	must(t, err)
	svc, err := New(Config{Pool: d.App, Log: log, Tayfas: tf, Events: ev, Memberships: ms})
	must(t, err)
	return &env{pool: d.App, admin: d.Admin, svc: svc, tayfas: tf, events: ev, members: ms}
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
		t.Fatalf("%s: hata = %v, beklenen %v", what, err, want)
	}
}

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("%s: hata = %v, beklenen doğrulama hatası", what, err)
	}
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// friends makes a and b friends (both get profiles).
func (e *env) friends(t *testing.T, a, b id.ID) {
	t.Helper()
	for _, u := range []id.ID{a, b} {
		e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, 'Kişi', $2) ON CONFLICT DO NOTHING`, u.PG(), id.New().PG())
	}
	low, high := social.Pair(a, b)
	e.exec(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, 'accepted', $1, now())`, low.PG(), high.PG())
}

func (e *env) friend(t *testing.T, of id.ID) id.ID {
	t.Helper()
	u := id.New()
	e.friends(t, of, u)
	return u
}

func input(title string, invitees ...id.ID) Input {
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	return Input{
		Event: event.Input{
			Type: "general", Title: title, StartsAt: start, EndsAt: start.Add(3 * time.Hour),
			Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.org/bulusma"},
			ClientRequestID: id.New().String(),
		},
		Invitees: invitees,
	}
}

func (e *env) membership(t *testing.T, tf, u id.ID) string {
	t.Helper()
	var role, st string
	must(t, e.pool.QueryRow(ctx, `SELECT role, status FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG()).Scan(&role, &st))
	return st + "/" + role
}

func TestCreate(t *testing.T) {
	e := setup(t)
	org := id.New()
	f1, f2 := e.friend(t, org), e.friend(t, org)

	in := input("  Akşam yemeği  ", f1, f2)
	r, err := e.svc.Create(ctx, org, in)
	must(t, err)
	v := r.Tayfa
	if v.Kind != access.KindInstant || v.Name != "Akşam yemeği" || v.JoinPolicy != tayfa.JoinApproval || v.MemberCount != 1 ||
		v.InstantEventID != r.Event.ID || v.JoinCode != "" {
		t.Fatalf("tayfa = %+v", v)
	}
	for _, m := range v.Modules {
		if m.Enabled != (m.Module == access.ModuleEvents) {
			t.Errorf("modül %s = %v", m.Module, m.Enabled)
		}
	}
	if r.Event.TayfaID != v.ID || r.Event.Title != "Akşam yemeği" || r.Event.CreatedBy != org || !r.Event.CallerCanManage {
		t.Fatalf("etkinlik = %+v", r.Event)
	}
	if got := e.membership(t, v.ID, org); got != "active/admin" {
		t.Fatalf("organizatör = %s", got)
	}
	for _, f := range []id.ID{f1, f2} {
		if got := e.membership(t, v.ID, f); got != "invited/member" {
			t.Fatalf("davetli = %s", got)
		}
	}
	// One tayfa.created with the kind, the event and the invitations.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.created' AND tayfa_id = $1 AND payload ->> 'kind' = 'instant'`, v.ID.PG()); n != 1 {
		t.Errorf("tayfa.created = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.created' AND tayfa_id = $1`, v.ID.PG()); n != 1 {
		t.Errorf("event.created = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.invited' AND tayfa_id = $1`, v.ID.PG()); n != 2 {
		t.Errorf("tayfa.invited = %d", n)
	}

	// A retry returns the first tayfa and event and creates nothing.
	again, err := e.svc.Create(ctx, org, in)
	must(t, err)
	if again.Tayfa.ID != v.ID || again.Event.ID != r.Event.ID {
		t.Fatalf("tekrar = %+v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE created_by = $1`, org.PG()); n != 1 {
		t.Fatalf("tayfa sayısı %d", n)
	}

	// The list marks it.
	list, _, err := e.tayfas.ListMine(ctx, org, 0, "")
	must(t, err)
	if len(list) != 1 || list[0].Kind != access.KindInstant || list[0].Tayfa.InstantEventID != r.Event.ID {
		t.Fatalf("liste = %+v", list)
	}

	// The invited friend accepts and is a member at once (the organizer
	// invited them).
	m, err := e.members.AcceptInvitation(ctx, f1, v.ID)
	must(t, err)
	if m.Status != authz.StatusActive {
		t.Fatalf("kabul = %+v", m)
	}
	l1, _, err := e.tayfas.ListMine(ctx, f1, 0, "")
	must(t, err)
	if len(l1) != 1 || l1[0].Kind != access.KindInstant {
		t.Fatalf("davetlinin listesi = %+v", l1)
	}

	// The tayfa is named after the event: a title change renames it.
	_, err = e.events.Update(ctx, org, v.ID, r.Event.ID, []string{event.FieldTitle}, event.Input{Title: "Pizza akşamı"})
	must(t, err)
	got, _, err := e.tayfas.Get(ctx, org, v.ID)
	must(t, err)
	if got.Name != "Pizza akşamı" {
		t.Fatalf("ad = %q", got.Name)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	stranger := id.New()
	e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, 'Yabancı', $2)`, stranger.PG(), id.New().PG())

	// A non-friend cannot be invited directly; nothing is created.
	_, err := e.svc.Create(ctx, org, input("Yürüyüş", f, stranger))
	wantErr(t, "arkadaş değil", err, apperr.ErrNotFriends)
	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE created_by = $1`, org.PG()); n != 0 {
		t.Fatalf("yarım kalan tayfa %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE created_by = $1`, org.PG()); n != 0 {
		t.Fatalf("yarım kalan etkinlik %d", n)
	}

	for name, in := range map[string]Input{
		"kendini":    input("Yürüyüş", org),
		"tekrar":     input("Yürüyüş", f, f),
		"boş kimlik": input("Yürüyüş", id.ID{}),
		"başlık":     input(""),
	} {
		_, err := e.svc.Create(ctx, org, in)
		wantInvalid(t, name, err)
	}
	many := input("Kalabalık")
	for range MaxInvitees + 1 {
		many.Invitees = append(many.Invitees, id.New())
	}
	_, err = e.svc.Create(ctx, org, many)
	wantInvalid(t, "fazla davetli", err)
	rec := input("Seri")
	rec.Event.Recurrence = &event.Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "Europe/Istanbul"}
	_, err = e.svc.Create(ctx, org, rec)
	wantInvalid(t, "tekrar eden", err)
	disc := input("Keşfet")
	disc.Event.Discoverable = true
	_, err = e.svc.Create(ctx, org, disc)
	wantInvalid(t, "keşfet", err)
	cover := input("Kapak")
	cover.Event.CoverUploadID = id.New()
	_, err = e.svc.Create(ctx, org, cover)
	wantInvalid(t, "kapak", err)
}

func TestOpenLimit(t *testing.T) {
	e := setup(t)
	org := id.New()
	var first Result
	for i := range MaxOpen {
		r, err := e.svc.Create(ctx, org, input("Etkinlik"))
		must(t, err)
		if i == 0 {
			first = r
		}
	}
	_, err := e.svc.Create(ctx, org, input("Dördüncü"))
	wantErr(t, "dördüncü", err, apperr.ErrInstantEventLimit)

	// A cancelled event is no longer open.
	_, err = e.events.Cancel(ctx, org, first.Tayfa.ID, first.Event.ID, "")
	must(t, err)
	_, err = e.svc.Create(ctx, org, input("Yeni"))
	must(t, err)
	_, err = e.svc.Create(ctx, org, input("Beşinci"))
	wantErr(t, "yine sınır", err, apperr.ErrInstantEventLimit)

	// Instant events are not tayfa creations: the daily tayfa limit is
	// untouched, and a standard tayfa does not use an instant slot.
	other := id.New()
	_, err = e.tayfas.Create(ctx, other, tayfa.CreateInput{Name: "Kalıcı Tayfa", ClientRequestID: id.New().String()})
	must(t, err)
	_, err = e.svc.Create(ctx, other, input("Anlık"))
	must(t, err)
	_, err = e.tayfas.Create(ctx, org, tayfa.CreateInput{Name: "Organizatörün Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
}

// TestOpenLimitConcurrent: concurrent creates by one user never pass the
// limit.
func TestOpenLimitConcurrent(t *testing.T) {
	e := setup(t)
	org := id.New()
	const n = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok, lim int
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Create(ctx, org, input("Aynı anda"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, apperr.ErrInstantEventLimit):
				lim++
			default:
				t.Errorf("beklenmeyen hata: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != MaxOpen || lim != n-MaxOpen {
		t.Fatalf("başarılı %d, sınır %d", ok, lim)
	}
	if c := e.count(t, `SELECT count(*) FROM tayfas WHERE created_by = $1 AND kind = 'instant'`, org.PG()); c != MaxOpen {
		t.Fatalf("anlık tayfa %d", c)
	}
}

// TestInstantRules: the permission table and the join paths of an instant
// tayfa (ADR-0032 §3, §4).
func TestInstantRules(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	r, err := e.svc.Create(ctx, org, input("Halı saha", f))
	must(t, err)
	tf := r.Tayfa.ID
	_, err = e.members.AcceptInvitation(ctx, f, tf)
	must(t, err)

	// Single event, fixed settings, no role changes.
	_, err = e.events.Create(ctx, org, tf, input("İkinci").Event)
	wantErr(t, "ikinci etkinlik", err, apperr.ErrPermissionDenied)
	err = e.tayfas.SetModule(ctx, org, tf, access.ModuleQA, true)
	wantErr(t, "modül", err, apperr.ErrPermissionDenied)
	_, err = e.tayfas.Update(ctx, org, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldName}, Name: "Başka Ad"})
	wantErr(t, "ayar", err, apperr.ErrPermissionDenied)
	_, err = e.tayfas.RotateJoinCode(ctx, org, tf)
	wantErr(t, "kod", err, apperr.ErrPermissionDenied)
	_, err = e.members.ChangeRole(ctx, org, tf, f, authz.Moderator)
	wantErr(t, "rol", err, apperr.ErrPermissionDenied)
	_, err = e.events.Update(ctx, org, tf, r.Event.ID, []string{event.FieldDiscoverable}, event.Input{Discoverable: true})
	wantInvalid(t, "keşfete açma", err)

	// Only the organizer invites.
	ff := e.friend(t, f)
	_, err = e.members.InviteFriend(ctx, f, tf, ff)
	wantErr(t, "üyenin daveti", err, apperr.ErrPermissionDenied)
	_, _, err = e.members.CreateInviteLink(ctx, f, tf, 24*time.Hour, 10)
	wantErr(t, "üyenin linki", err, apperr.ErrPermissionDenied)

	// A link holder sends a join request, even though the organizer (an
	// admin) made the link; the organizer approves.
	_, token, err := e.members.CreateInviteLink(ctx, org, tf, 24*time.Hour, 10)
	must(t, err)
	guest := id.New()
	m, err := e.members.JoinByInvite(ctx, guest, token)
	must(t, err)
	if m.Status != authz.StatusPending {
		t.Fatalf("linkle katılım = %+v", m)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND tayfa_id = $1
		AND payload ->> 'type' = 'join_request' AND payload -> 'recipient_ids' = jsonb_build_array($2::text)`, tf.PG(), org.String()); n != 1 {
		t.Fatalf("katılım isteği bildirimi = %d", n)
	}
	_, err = e.members.ApproveMember(ctx, org, tf, guest)
	must(t, err)
	if got := e.membership(t, tf, guest); got != "active/member" {
		t.Fatalf("onaylanan = %s", got)
	}

	// The join code is never usable.
	var code string
	must(t, e.pool.QueryRow(ctx, `SELECT join_code FROM tayfas WHERE id = $1`, tf.PG()).Scan(&code))
	_, err = e.members.JoinByCode(ctx, id.New(), code)
	wantErr(t, "kodla katılım", err, apperr.ErrInvalidJoinCode)

	// The organizer still moderates.
	_, err = e.members.MuteMember(ctx, org, tf, f, time.Now().Add(time.Hour))
	must(t, err)
}

func TestDatabaseGuards(t *testing.T) {
	e := setup(t)
	org := id.New()
	r, err := e.svc.Create(ctx, org, input("Kamp"))
	must(t, err)
	tf := r.Tayfa.ID

	for name, sql := range map[string]string{
		"listelenemez":     `UPDATE tayfas SET visibility = 'listed' WHERE id = $1`,
		"açık katılım yok": `UPDATE tayfas SET join_policy = 'open' WHERE id = $1`,
		"dönüşüm zamansız": `UPDATE tayfas SET kind = 'standard' WHERE id = $1`,
		"zaman türsüz":     `UPDATE tayfas SET converted_at = now() WHERE id = $1`,
		"ikinci etkinlik": `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url)
			SELECT gen_random_uuid(), $1, 'general', 'online', 'İkinci', now() + interval '1 day', now() + interval '2 days', 'https://example.org/y'`,
	} {
		if _, err := e.pool.Exec(ctx, sql, tf.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	// Converted once, it stays standard with its conversion time.
	e.exec(t, `UPDATE tayfas SET kind = 'standard', converted_at = now(), name = 'Kamp Tayfası', name_normalized = 'kamp tayfası' WHERE id = $1`, tf.PG())
	for name, sql := range map[string]string{
		"geri dönüş":     `UPDATE tayfas SET kind = 'instant' WHERE id = $1`,
		"zaman değişmez": `UPDATE tayfas SET converted_at = now() + interval '1 hour' WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, tf.PG()); store.SQLState(err) != "TF120" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestConvert(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	r, err := e.svc.Create(ctx, org, input("Bisiklet turu", f))
	must(t, err)
	tf := r.Tayfa.ID
	_, err = e.members.AcceptInvitation(ctx, f, tf)
	must(t, err)

	in := tayfa.ConvertInput{Name: "Bisiklet Tayfası", Interests: []string{"sports", "hike"}}
	// Members cannot convert; a standard tayfa cannot be converted.
	_, err = e.tayfas.Convert(ctx, f, tf, in)
	wantErr(t, "üye", err, apperr.ErrPermissionDenied)
	std, err := e.tayfas.Create(ctx, id.New(), tayfa.CreateInput{Name: "Kalıcı", ClientRequestID: id.New().String()})
	must(t, err)
	var stdAdmin id.ID
	must(t, e.pool.QueryRow(ctx, `SELECT created_by FROM tayfas WHERE id = $1`, std.ID.PG()).Scan(&stdAdmin))
	_, err = e.tayfas.Convert(ctx, stdAdmin, std.ID, in)
	wantErr(t, "kalıcı tayfa", err, apperr.ErrPermissionDenied)
	_, err = e.tayfas.Convert(ctx, org, tf, tayfa.ConvertInput{Name: "ab"})
	wantInvalid(t, "kısa ad", err)

	v, err := e.tayfas.Convert(ctx, org, tf, in)
	must(t, err)
	if v.Kind != access.KindStandard || v.Name != "Bisiklet Tayfası" || v.InstantEventID != (id.ID{}) ||
		len(v.Interests) != 2 || v.MemberCount != 2 || v.JoinCode == "" {
		t.Fatalf("dönüşen = %+v", v)
	}
	for _, m := range v.Modules {
		if !m.Enabled {
			t.Errorf("modül %s kapalı", m.Module)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = 'tayfa.converted' AND actor_id = $2`, tf.PG(), org.PG()); n != 1 {
		t.Errorf("denetim kaydı %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE tayfa_id = $1 AND type = 'tayfa.updated' AND payload -> 'fields' ? 'kind'`, tf.PG()); n != 1 {
		t.Errorf("tayfa.updated %d", n)
	}

	// Standard rules from now on: more events, member links, roles.
	_, err = e.events.Create(ctx, org, tf, input("İkinci tur").Event)
	must(t, err)
	_, _, err = e.members.CreateInviteLink(ctx, f, tf, 24*time.Hour, 10)
	must(t, err)
	_, err = e.members.ChangeRole(ctx, org, tf, f, authz.Moderator)
	must(t, err)
	// A second conversion is a repeat (TestConvertRepeat), not a new one.
	if again, err := e.tayfas.Convert(ctx, org, tf, in); err != nil || again.ID != tf {
		t.Fatalf("ikinci dönüşüm: %+v %v", again, err)
	}

	// It counts as creating a tayfa: the organizer's daily limit is used.
	_, err = e.tayfas.Create(ctx, org, tayfa.CreateInput{Name: "Bir Tayfa Daha", ClientRequestID: id.New().String()})
	wantErr(t, "günlük sınır", err, apperr.ErrTayfaCreateLimit)

	// And a tayfa created today blocks a conversion.
	org2 := id.New()
	r2, err := e.svc.Create(ctx, org2, input("Sinema"))
	must(t, err)
	_, err = e.tayfas.Create(ctx, org2, tayfa.CreateInput{Name: "Sinemacılar", ClientRequestID: id.New().String()})
	must(t, err)
	_, err = e.tayfas.Convert(ctx, org2, r2.Tayfa.ID, in)
	wantErr(t, "sınırdan sonra", err, apperr.ErrTayfaCreateLimit)

	// An archived instant tayfa is read-only.
	org3 := id.New()
	r3, err := e.svc.Create(ctx, org3, input("Konser"))
	must(t, err)
	e.exec(t, `UPDATE tayfas SET status = 'archived', archived_at = now() WHERE id = $1`, r3.Tayfa.ID.PG())
	_, err = e.tayfas.Convert(ctx, org3, r3.Tayfa.ID, in)
	wantErr(t, "arşiv", err, apperr.ErrTayfaReadOnly)
}

// TestDailyLimit: at most MaxPerDay instant events in 24 hours, cancelled
// ones included, so create and cancel cannot make hidden tayfas without end.
func TestDailyLimit(t *testing.T) {
	e := setup(t)
	org := id.New()
	for range MaxPerDay {
		r, err := e.svc.Create(ctx, org, input("Döngü"))
		must(t, err)
		_, err = e.events.Cancel(ctx, org, r.Tayfa.ID, r.Event.ID, "")
		must(t, err)
	}
	_, err := e.svc.Create(ctx, org, input("Altıncı"))
	wantErr(t, "günlük sınır", err, apperr.ErrInstantEventLimit)
	// A day later the window has moved on.
	e.exec(t, `UPDATE tayfas SET created_at = created_at - interval '25 hours' WHERE created_by = $1`, org.PG())
	_, err = e.svc.Create(ctx, org, input("Ertesi gün"))
	must(t, err)
}

// TestRequestIDShared: tayfa creates and instant events share the request
// ID space; reusing one across them is a validation error, never a 500.
func TestRequestIDShared(t *testing.T) {
	e := setup(t)
	u := id.New()
	in := input("Ortak kimlik")
	_, err := e.tayfas.Create(ctx, u, tayfa.CreateInput{Name: "Kalıcı Tayfa", ClientRequestID: in.Event.ClientRequestID})
	must(t, err)
	_, err = e.svc.Create(ctx, u, in)
	wantInvalid(t, "kalıcının kimliği", err)

	v := id.New()
	in2 := input("Önce anlık")
	_, err = e.svc.Create(ctx, v, in2)
	must(t, err)
	_, err = e.tayfas.Create(ctx, v, tayfa.CreateInput{Name: "Sonra Kalıcı", ClientRequestID: in2.Event.ClientRequestID})
	wantInvalid(t, "anlığın kimliği", err)
}

// TestDatabaseGuardsEventAndRoles: the instant event never becomes
// discoverable and nobody becomes moderator, whatever the app does.
func TestDatabaseGuardsEventAndRoles(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	r, err := e.svc.Create(ctx, org, input("Konser", f))
	must(t, err)
	_, err = e.members.AcceptInvitation(ctx, f, r.Tayfa.ID)
	must(t, err)
	if _, err := e.pool.Exec(ctx, `UPDATE events SET discoverable = true WHERE id = $1`, r.Event.ID.PG()); store.SQLState(err) != "TF120" {
		t.Fatalf("keşfete açma: %v", err)
	}
	// Even with the membership trigger off (superuser), no moderator.
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `ALTER TABLE memberships DISABLE TRIGGER memberships_guard`)
	must(t, err)
	if _, err := tx.Exec(ctx, `UPDATE memberships SET role = 'moderator' WHERE tayfa_id = $1 AND user_id = $2`, r.Tayfa.ID.PG(), f.PG()); store.SQLState(err) != "TF120" {
		t.Fatalf("moderatör: %v", err)
	}
}

// TestOrganizerDeleted: when the organizer deletes their account, the admin
// who takes over is the organizer: they invite and convert (ADR-0032 §3).
func TestOrganizerDeleted(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	r, err := e.svc.Create(ctx, org, input("Piknik", f))
	must(t, err)
	tf := r.Tayfa.ID
	_, err = e.members.AcceptInvitation(ctx, f, tf)
	must(t, err)

	ue, err := userevents.New(userevents.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	must(t, ue.Apply(ctx, userevents.Event{ID: id.New(), User: org, OccurredAt: time.Now(), Deleted: true}))
	if got := e.membership(t, tf, f); got != "active/admin" {
		t.Fatalf("devralan = %s", got)
	}

	// The heir invites and converts; the tayfa counts on their limit.
	ff := e.friend(t, f)
	_, err = e.members.InviteFriend(ctx, f, tf, ff)
	must(t, err)
	v, err := e.tayfas.Convert(ctx, f, tf, tayfa.ConvertInput{Name: "Piknikçiler"})
	must(t, err)
	if v.Kind != access.KindStandard {
		t.Fatalf("tür = %s", v.Kind)
	}
	var by id.ID
	must(t, e.pool.QueryRow(ctx, `SELECT created_by FROM tayfas WHERE id = $1`, tf.PG()).Scan(&by))
	if by != f {
		t.Fatalf("created_by = %s", by)
	}
	_, err = e.tayfas.Create(ctx, f, tayfa.CreateInput{Name: "Bir Tayfa Daha", ClientRequestID: id.New().String()})
	wantErr(t, "devralanın sınırı", err, apperr.ErrTayfaCreateLimit)
}

// TestConvertRepeat: a repeat of a conversion that went through returns the
// converted tayfa to its admin and changes nothing.
func TestConvertRepeat(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	r, err := e.svc.Create(ctx, org, input("Kahvaltı", f))
	must(t, err)
	_, err = e.members.AcceptInvitation(ctx, f, r.Tayfa.ID)
	must(t, err)
	in := tayfa.ConvertInput{Name: "Kahvaltıcılar"}
	first, err := e.tayfas.Convert(ctx, org, r.Tayfa.ID, in)
	must(t, err)
	again, err := e.tayfas.Convert(ctx, org, r.Tayfa.ID, tayfa.ConvertInput{Name: "Başka Ad"})
	must(t, err)
	if again.ID != first.ID || again.Name != "Kahvaltıcılar" {
		t.Fatalf("tekrar = %+v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = 'tayfa.converted'`, r.Tayfa.ID.PG()); n != 1 {
		t.Fatalf("denetim kaydı %d", n)
	}
	// A member still cannot.
	_, err = e.tayfas.Convert(ctx, f, r.Tayfa.ID, in)
	wantErr(t, "üye", err, apperr.ErrPermissionDenied)
}
