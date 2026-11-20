package guest_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types: gameType a board game night whose scoreboard takes guests and
// a draw; plainType a scoreboard without guests; gameV2 the second version
// of gameType without the scoreboard.
const (
	gameType  = "test_guest_games"
	gameV1    = `{"components":[{"kind":"scoreboard","params":{"tables":true,"guests":true}},{"kind":"draw"}]}`
	gameV2    = `{"components":[{"kind":"draw"}]}`
	plainType = "test_plain_games"
	plainV1   = `{"components":[{"kind":"scoreboard","params":{"tables":true}}]}`
	key       = "scoreboard"
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
	guests *guest.Service
	sb     *scoreboard.Service
	draws  *draw.Service
	fw     *component.Service
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
	e := &env{
		guests: guest.New(fw), sb: scoreboard.New(fw), draws: draw.New(fw), fw: fw,
		events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c,
	}
	for k, def := range map[string]string{gameType: gameV1, plainType: plainV1} {
		e.addType(t, k, def)
	}
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

func (e *env) addType(t *testing.T, k, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['games'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, k, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, k)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Oyun Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

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

// going adds n members who answered "going".
func (e *env) going(t *testing.T, tf, ev id.ID, n int) []id.ID {
	t.Helper()
	out := make([]id.ID, 0, n)
	for range n {
		u := e.member(t, tf, authz.Member)
		_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
		must(t, err)
		out = append(out, u)
	}
	return out
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Oyun gecesi", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(),
	})
	must(t, err)
	return v
}

func ref(user, tf, ev id.ID) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func lastGuest(g guest.Guests) guest.Guest { return g.Guests[len(g.Guests)-1] }

func TestCleanNickname(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Ali", "Ali"},
		{"  Ayşe   Teyze ", "Ayşe Teyze"},
		{strings.Repeat("ğ", 24), strings.Repeat("ğ", 24)},
		{"Kuzen\u00a0Mehmet", "Kuzen Mehmet"},
		{"İpek", "İpek"},
		{strings.Repeat("a", 23) + " ", strings.Repeat("a", 23)},
	} {
		if got, ok := guest.CleanNickname(tc.in); !ok || got != tc.want {
			t.Errorf("%q: %q %v, %q bekleniyordu", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{
		"", "   ", strings.Repeat("a", 25), "Ali\nVeli", "Ali\tVeli", "Ali\u200bVeli", "Ali\u202eVeli",
		"\xff", strings.Repeat(" ", 1000) + "a",
	} {
		if got, ok := guest.CleanNickname(in); ok {
			t.Errorf("%q kabul edildi: %q", in, got)
		}
	}
}

// Adding, listing and removing guests; the limit, the nickname rules and a
// live update for every change; the nickname never reaches an audit record
// or an outbox event.
func TestGuests(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	r := ref(admin, tf, ev.ID)

	got, err := e.guests.Get(ctx, r)
	must(t, err)
	if len(got.Guests) != 0 || got.MaxGuests != guest.MaxGuests || !got.CallerCanAdd || !got.CallerCanRemove {
		t.Fatalf("boş liste: %+v", got)
	}
	got, err = e.guests.Add(ctx, r, guest.Input{Nickname: "  Kuzen   Ali "})
	must(t, err)
	ali := lastGuest(got)
	if len(got.Guests) != 1 || ali.Nickname != "Kuzen Ali" || ali.CreatedAt.IsZero() {
		t.Fatalf("eklenen: %+v", got)
	}
	for _, bad := range []string{"", strings.Repeat("x", 25), "Ali\u200b"} {
		_, err = e.guests.Add(ctx, r, guest.Input{Nickname: bad})
		wantField(t, "takma ad "+bad, err, "nickname")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2`, component.EventChanged, ev.ID.PG()); n != 1 {
		t.Errorf("canlı olay %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND metadata ->> 'guest_id' = $2`, guest.AuditAdded, ali.ID.String()); n != 1 {
		t.Errorf("denetim kaydı %d", n)
	}
	for _, sql := range []string{
		`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Kuzen%'`,
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Kuzen%'`,
	} {
		if n := e.count(t, sql); n != 0 {
			t.Errorf("takma ad sızdı: %s", sql)
		}
	}

	// At most MaxGuests per event.
	for i := 1; i < guest.MaxGuests; i++ {
		_, err = e.guests.Add(ctx, r, guest.Input{Nickname: "Misafir"})
		must(t, err)
	}
	_, err = e.guests.Add(ctx, r, guest.Input{Nickname: "Fazla"})
	wantErr(t, "sınır", err, apperr.ErrEventLimit)

	got, err = e.guests.Remove(ctx, r, ali.ID)
	must(t, err)
	if len(got.Guests) != guest.MaxGuests-1 {
		t.Fatalf("silme sonrası %d", len(got.Guests))
	}
	_, err = e.guests.Remove(ctx, r, ali.ID)
	wantErr(t, "silinmiş misafir", err, apperr.ErrGuestPlayerNotFound)
	// Another event's guest is not this event's.
	other := e.newEvent(t, tf, admin, gameType)
	_, err = e.guests.Remove(ctx, ref(admin, tf, other.ID), got.Guests[0].ID)
	wantErr(t, "başka etkinliğin misafiri", err, apperr.ErrGuestPlayerNotFound)
}

// A scoreboard without guests, a type without the component and a newer
// type version refuse; the table's CHECK and keys hold.
func TestGuestsOutsideComponent(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	plain := e.newEvent(t, tf, admin, plainType)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	game := e.newEvent(t, tf, admin, gameType)

	_, err := e.guests.Get(ctx, ref(admin, tf, plain.ID))
	wantErr(t, "misafirsiz yazboz okuma", err, apperr.ErrGuestPlayersDisabled)
	_, err = e.guests.Add(ctx, ref(admin, tf, plain.ID), guest.Input{Nickname: "Ali"})
	wantErr(t, "misafirsiz yazboz", err, apperr.ErrGuestPlayersDisabled)
	_, err = e.guests.Add(ctx, ref(admin, tf, general.ID), guest.Input{Nickname: "Ali"})
	wantErr(t, "bileşensiz tür", err, apperr.ErrComponentNotFound)

	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, gameType, gameV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, gameType, gameV2)
	newer := e.newEvent(t, tf, admin, gameType)
	_, err = e.guests.Add(ctx, ref(admin, tf, newer.ID), guest.Input{Nickname: "Ali"})
	wantErr(t, "yeni sürüm", err, apperr.ErrComponentNotFound)
	if _, err := e.guests.Add(ctx, ref(admin, tf, game.ID), guest.Input{Nickname: "Ali"}); err != nil {
		t.Fatalf("eski sürümün etkinliği: %v", err)
	}

	for name, tc := range map[string]struct{ sql, constraint string }{
		"uzun takma ad": {`INSERT INTO event_guest_players (id, event_id, nickname) VALUES (gen_random_uuid(), $1, '` + strings.Repeat("a", 25) + `')`, "event_guest_players_nickname"},
		"boş takma ad":  {`INSERT INTO event_guest_players (id, event_id, nickname) VALUES (gen_random_uuid(), $1, '')`, "event_guest_players_nickname"},
		"boşluklu":      {`INSERT INTO event_guest_players (id, event_id, nickname) VALUES (gen_random_uuid(), $1, ' Ali')`, "event_guest_players_nickname"},
		"denetim":       {`INSERT INTO event_guest_players (id, event_id, nickname) VALUES (gen_random_uuid(), $1, E'Ali\nVeli')`, "event_guest_players_nickname"},
	} {
		_, err := e.pool.Exec(ctx, tc.sql, game.ID.PG())
		if c := constraintOf(err); c != tc.constraint {
			t.Errorf("%s: %q (%v)", name, c, err)
		}
	}
	// core_app cannot move a guest or rename it.
	for name, sql := range map[string]string{
		"etkinlik": `UPDATE event_guest_players SET event_id = $1`,
		"zaman":    `UPDATE event_guest_players SET created_at = now() WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id.New().PG()); err == nil {
			t.Errorf("%s değiştirildi", name)
		}
	}
}

// Who may add and remove: managers and the referees of the event, the
// component or the given table add; managers remove. A cancelled or frozen
// event takes nothing; reads stay open.
func TestGuestPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	compRef := e.member(t, tf, authz.Member)
	tableRef := e.member(t, tf, authz.Member)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)

	b, err := e.sb.CreateTable(ctx, ref(admin, tf, ev.ID), scoreboard.TableInput{Label: "Masa 1"})
	must(t, err)
	table := b.Tables[0].ID
	b, err = e.sb.CreateTable(ctx, ref(admin, tf, ev.ID), scoreboard.TableInput{Label: "Masa 2"})
	must(t, err)
	table2 := b.Tables[1].ID
	for _, in := range []component.RefereeInput{
		{User: compRef, ComponentKey: key},
		{User: tableRef, ComponentKey: key, Scope: table},
		{User: muted, ComponentKey: key},
	} {
		_, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, in)
		must(t, err)
	}

	for _, tc := range []struct {
		name  string
		user  id.ID
		table id.ID
		want  error
	}{
		{"yönetici", admin, id.ID{}, nil},
		{"moderatör", moderator, id.ID{}, nil},
		{"bileşenin hakemi", compRef, id.ID{}, nil},
		{"masanın hakemi, kendi masası", tableRef, table, nil},
		{"masanın hakemi, masasız", tableRef, id.ID{}, apperr.ErrPermissionDenied},
		{"masanın hakemi, başka masa", tableRef, table2, apperr.ErrPermissionDenied},
		{"üye", member, id.ID{}, apperr.ErrPermissionDenied},
		{"üye, masayla", member, table, apperr.ErrPermissionDenied},
		{"susturulmuş hakem", muted, id.ID{}, apperr.ErrMuted},
		{"üye değil", id.New(), id.ID{}, apperr.ErrTayfaNotFound},
		{"bileşende olmayan masa", admin, id.New(), apperr.ErrScoreTableNotFound},
	} {
		_, err := e.guests.Add(ctx, ref(tc.user, tf, ev.ID), guest.Input{Nickname: "Misafir", Table: tc.table})
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) && (!errors.Is(tc.want, apperr.ErrMuted) || !errors.Is(err, apperr.ErrPermissionDenied)) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}

	got, err := e.guests.Get(ctx, ref(compRef, tf, ev.ID))
	must(t, err)
	if !got.CallerCanAdd || got.CallerCanRemove {
		t.Errorf("bileşen hakemi: %+v", got)
	}
	got, err = e.guests.Get(ctx, ref(member, tf, ev.ID))
	must(t, err)
	if got.CallerCanAdd || got.CallerCanRemove || len(got.Guests) != 4 {
		t.Errorf("üye: %+v", got)
	}
	g := got.Guests[0].ID
	for _, u := range []id.ID{member, compRef, tableRef} {
		_, err := e.guests.Remove(ctx, ref(u, tf, ev.ID), g)
		wantErr(t, "yönetici olmayanın silmesi", err, apperr.ErrPermissionDenied)
	}
	_, err = e.guests.Get(ctx, ref(admin, id.New(), ev.ID))
	if err == nil {
		t.Error("başka tayfadan okundu")
	}

	// Frozen and cancelled events take nothing; reads stay open.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.guests.Add(ctx, ref(admin, tf, ev.ID), guest.Input{Nickname: "Geç"})
	wantErr(t, "donmuş ekleme", err, apperr.ErrComponentFrozen)
	_, err = e.guests.Remove(ctx, ref(admin, tf, ev.ID), g)
	wantErr(t, "donmuş silme", err, apperr.ErrComponentFrozen)
	if got, err := e.guests.Get(ctx, ref(admin, tf, ev.ID)); err != nil || got.CallerCanAdd || got.CallerCanRemove {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, gameType)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.guests.Add(ctx, ref(admin, tf, cancelled.ID), guest.Input{Nickname: "Ali"})
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
}

// A guest sits at a player table of a component with guests and plays;
// a seated guest is not removed; the draw takes guests with the
// participants and keeps their places.
func TestGuestSeatsAndDraw(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	r := ref(admin, tf, ev.ID)
	p := e.going(t, tf, ev.ID, 1)
	gs, err := e.guests.Add(ctx, r, guest.Input{Nickname: "Ali"})
	must(t, err)
	ali := lastGuest(gs)
	gs, err = e.guests.Add(ctx, r, guest.Input{Nickname: "Veli"})
	must(t, err)
	veli := lastGuest(gs)

	b, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa", Players: []scoreboard.PlayerInput{{User: p[0]}, {Guest: ali.ID}}})
	must(t, err)
	tb := b.Tables[0]
	if len(tb.Players) != 2 || tb.Players[1].Guest != ali.ID || tb.Players[1].User != (id.ID{}) || tb.Players[0].User != p[0] {
		t.Fatalf("masa: %+v", tb.Players)
	}
	for name, tc := range map[string]struct {
		in   []scoreboard.PlayerInput
		want error
	}{
		"başka etkinliğin misafiri": {[]scoreboard.PlayerInput{{User: p[0]}, {Guest: id.New()}}, apperr.ErrGuestPlayerNotFound},
		"iki kez":                   {[]scoreboard.PlayerInput{{Guest: ali.ID}, {Guest: ali.ID}}, nil},
		"hem kişi hem misafir":      {[]scoreboard.PlayerInput{{User: p[0], Guest: ali.ID}, {Guest: veli.ID}}, nil},
	} {
		_, err := e.sb.SetPlayers(ctx, r, tb.ID, tc.in)
		if tc.want == nil {
			wantField(t, name, err, "players")
		} else {
			wantErr(t, name, err, tc.want)
		}
	}

	e.clock.set(ev.StartsAt.Add(time.Minute))
	b, err = e.sb.AddRound(ctx, r, tb.ID, []scoreboard.ScoreInput{{Player: tb.Players[0].ID, Points: 3}, {Player: tb.Players[1].ID, Points: 5}})
	must(t, err)
	if st := b.Tables[0].Result.Standings; len(st) != 2 || st[0].Player != tb.Players[1].ID {
		t.Fatalf("misafirin skoru: %+v", st)
	}
	_, err = e.guests.Remove(ctx, r, ali.ID)
	wantErr(t, "masadaki misafir", err, apperr.ErrGuestPlayerInUse)

	// A scoreboard without guests seats none.
	plain := e.newEvent(t, tf, admin, plainType)
	_, err = e.sb.CreateTable(ctx, ref(admin, tf, plain.ID), scoreboard.TableInput{Label: "Masa", Players: []scoreboard.PlayerInput{{Guest: ali.ID}}})
	wantErr(t, "misafirsiz yazboz", err, apperr.ErrGuestPlayersDisabled)

	// The draw takes the participant and both guests.
	d, err := e.draws.Make(ctx, component.Ref{User: admin, Tayfa: tf, Event: ev.ID, Key: "draw"})
	must(t, err)
	seen := map[id.ID]bool{}
	for _, en := range d.Entries {
		if en.Empty() {
			t.Errorf("boş yer: %+v", en)
		}
		seen[en.User], seen[en.Guest] = true, true
	}
	if len(d.Entries) != 3 || !seen[p[0]] || !seen[ali.ID] || !seen[veli.ID] {
		t.Fatalf("kura: %+v", d.Entries)
	}
	_, err = e.guests.Remove(ctx, r, veli.ID)
	wantErr(t, "kuradaki misafir", err, apperr.ErrGuestPlayerInUse)

	// A guest's place is fixed: it never moves to another guest or empties.
	for name, sql := range map[string]string{
		"başka misafir": `UPDATE event_draw_entries SET guest_id = $2 WHERE event_id = $1 AND guest_id IS NOT NULL`,
		"boşaltma":      `UPDATE event_draw_entries SET guest_id = NULL WHERE event_id = $1 AND guest_id IS NOT NULL AND $2::uuid IS NOT NULL`,
	} {
		_, err := e.admin.Exec(ctx, sql, ev.ID.PG(), veli.ID.PG())
		if c := constraintOf(err); c != "event_draw_entries_fixed" {
			t.Errorf("%s: %q (%v)", name, c, err)
		}
	}
	// A seat is a person's, a team's or a guest's, never two.
	_, err = e.admin.Exec(ctx, `UPDATE event_score_players SET user_id = $2 WHERE event_id = $1 AND guest_id IS NOT NULL`, ev.ID.PG(), p[0].PG())
	if c := constraintOf(err); c != "event_score_players_one" {
		t.Errorf("iki sahipli yer: %q (%v)", c, err)
	}

	// A cleared nickname keeps the guest, its seat and its place.
	e.exec(t, `UPDATE event_guest_players SET nickname = NULL WHERE event_id = $1`, ev.ID.PG())
	gs, err = e.guests.Get(ctx, r)
	must(t, err)
	if len(gs.Guests) != 2 || gs.Guests[0].Nickname != "" {
		t.Fatalf("takma adı silinmiş misafirler: %+v", gs)
	}
	b, err = e.sb.Get(ctx, r)
	must(t, err)
	if b.Tables[0].Players[1].Guest != ali.ID {
		t.Fatalf("misafirin yeri: %+v", b.Tables[0].Players)
	}
	dr, err := e.draws.Get(ctx, component.Ref{User: admin, Tayfa: tf, Event: ev.ID, Key: "draw"})
	must(t, err)
	for _, en := range dr.Entries {
		if en.Empty() {
			t.Errorf("takma ad silinince boşalan yer: %+v", en)
		}
	}
}

// Account deletion clears the nicknames the person added and the adder;
// the export lists only one's own guests.
func TestGuestAccountDeletionAndExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	moderator := e.member(t, tf, authz.Moderator)
	_, err := e.guests.Add(ctx, ref(admin, tf, ev.ID), guest.Input{Nickname: "Benim Misafirim"})
	must(t, err)
	_, err = e.guests.Add(ctx, ref(moderator, tf, ev.ID), guest.Input{Nickname: "Onun Misafiri"})
	must(t, err)

	q := store.New(e.pool)
	rows, err := q.ExportGuestPlayers(ctx, store.ExportGuestPlayersParams{UserID: admin.PG(), After: pgtype.UUID{Bytes: [16]byte{}, Valid: true}, N: 10})
	must(t, err)
	if len(rows) != 1 || !strings.Contains(string(rows[0].Data), "Benim Misafirim") || strings.Contains(string(rows[0].Data), "Onun") ||
		!strings.Contains(string(rows[0].Data), ev.ID.String()) {
		t.Fatalf("dışa aktarma: %d %v", len(rows), rows)
	}

	must(t, q.ClearGuestPlayerCreator(ctx, admin.PG()))
	if n := e.count(t, `SELECT count(*) FROM event_guest_players WHERE event_id = $1`, ev.ID.PG()); n != 2 {
		t.Fatalf("misafir satırı %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_guest_players WHERE nickname IS NULL AND created_by IS NULL`); n != 1 {
		t.Errorf("silinen hesabın misafiri %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_guest_players WHERE nickname = 'Onun Misafiri' AND created_by = $1`, moderator.PG()); n != 1 {
		t.Errorf("diğer misafir %d", n)
	}
}
