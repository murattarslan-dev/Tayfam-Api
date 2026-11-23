package teams_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types (Notion Faz 3 kataloğu): sportType two teams, balanced by
// default and left to the organizer; diveType pairs (dive buddies) under
// its own key, and a second version without them; benchType two teams of
// at most two; optType an optional component that is off by default.
const (
	sportType = "test_team_sport"
	sportV1   = `{"components":[{"kind":"teams","params":{"mode":{"organizer_choice":["random","balanced","manual"],"default":"balanced"},"team_count":2}}]}`
	diveType  = "test_dive"
	diveV1    = `{"components":[{"kind":"teams","key":"buddies","params":{"mode":"random","team_size":2}}]}`
	diveV2    = `{"components":[{"kind":"draw"}]}`
	benchType = "test_bench"
	benchV1   = `{"components":[{"kind":"teams","params":{"mode":"random","team_count":2,"team_size":2}}]}`
	optType   = "test_opt"
	optV1     = `{"components":[{"kind":"teams","optional":{"default":false}}]}`
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
	teams  *teams.Service
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
	e := &env{teams: teams.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{sportType: sportV1, diveType: diveV1, benchType: benchV1, optType: optV1} {
		e.addType(t, key, def)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Takım Tayfası", ClientRequestID: id.New().String()})
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
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Halısaha", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
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

// placed returns everyone in the teams; it fails on a person in two teams.
func placed(t *testing.T, got teams.Teams) map[id.ID]int {
	t.Helper()
	out := map[id.ID]int{}
	for i, tm := range got.Teams {
		if tm.Position != int32(i+1) {
			t.Fatalf("takım sırası: %+v", got.Teams)
		}
		for _, u := range tm.Members {
			if _, dup := out[u]; dup {
				t.Fatalf("iki takımda: %s", u)
			}
			out[u] = i
		}
	}
	return out
}

func sizes(got teams.Teams) []int {
	var out []int
	for _, tm := range got.Teams {
		out = append(out, len(tm.Members))
	}
	slices.Sort(out)
	return out
}

// How participants are split, without the database.
func TestPlanAndDeal(t *testing.T) {
	for _, tc := range []struct {
		name               string
		n, count, size     int
		wantTeams, wantPly int
		err                error
	}{
		{"varsayılan iki takım", 7, 0, 0, 2, 7, nil},
		{"sayı", 9, 3, 0, 3, 9, nil},
		{"sayı ve kapasite: yedekler", 9, 2, 4, 2, 8, nil},
		{"kapasite yetiyor", 6, 2, 4, 2, 6, nil},
		{"ikili: tek kalan katılır", 5, 0, 2, 2, 5, nil},
		{"büyüklük üç kişiye bir takım", 3, 0, 2, 1, 3, nil},
		{"bireysel", 4, 0, 1, 4, 4, nil},
		{"en çok elli takım", 120, 0, 2, teams.MaxTeams, 120, nil},
		{"tek kişi", 1, 0, 0, 0, 0, apperr.ErrTeamsTooFew},
		{"takımdan az kişi", 3, 4, 0, 0, 0, apperr.ErrTeamsTooFew},
		{"büyüklükten az kişi", 4, 0, 5, 0, 0, apperr.ErrTeamsTooFew},
	} {
		p, err := teams.PlanFor(tc.n, tc.count, tc.size)
		if tc.err != nil {
			wantErr(t, tc.name, err, tc.err)
			continue
		}
		if err != nil || p.Teams != tc.wantTeams || p.Play != tc.wantPly {
			t.Errorf("%s: %+v %v", tc.name, p, err)
		}
	}

	ps := []id.ID{id.New(), id.New(), id.New(), id.New(), id.New()}
	snake := teams.Deal(ps, 2, true)
	if !slices.Equal(snake[0], []id.ID{ps[0], ps[3], ps[4]}) || !slices.Equal(snake[1], []id.ID{ps[1], ps[2]}) {
		t.Fatalf("yılan sırası: %v", snake)
	}
	round := teams.Deal(ps, 2, false)
	if !slices.Equal(round[0], []id.ID{ps[0], ps[2], ps[4]}) || !slices.Equal(round[1], []id.ID{ps[1], ps[3]}) {
		t.Fatalf("sırayla: %v", round)
	}
}

// Balanced teams from the levels sent; levels are never kept; teams keep
// their IDs and names by position; a person who stops being a participant
// drops out; every change is a live update and an audit record without
// people, names or levels.
func TestTeamsGenerateAndReplace(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, sportType)
	const key = "teams"
	r := ref(admin, tf, ev.ID, key)
	p := e.going(t, tf, ev.ID, 4)
	maybe := e.member(t, tf, authz.Member)
	_, err := e.events.SetRSVP(ctx, maybe, tf, ev.ID, event.RSVPMaybe)
	must(t, err)

	got, err := e.teams.Get(ctx, ref(maybe, tf, ev.ID, key))
	must(t, err)
	if len(got.Teams) != 0 || got.Teams == nil || got.Mode != teams.ModeBalanced || got.TeamCount != 2 || got.TeamSize != 0 || got.CallerCanEdit {
		t.Fatalf("boş takımlar: %+v", got)
	}

	// Two strong and two weak players: each team gets one of each.
	levels := []teams.Level{{User: p[0], Level: 5}, {User: p[1], Level: 5}, {User: p[2], Level: 1}, {User: p[3], Level: 1}, {User: maybe, Level: 4}}
	for range 5 {
		got, err = e.teams.Generate(ctx, r, levels)
		must(t, err)
		where := placed(t, got)
		if len(got.Teams) != 2 || len(where) != 4 || !got.CallerCanEdit {
			t.Fatalf("dengeli takımlar: %+v", got)
		}
		if _, in := where[maybe]; in {
			t.Fatal("katılımcı olmayan takıma alındı")
		}
		if where[p[0]] == where[p[1]] || where[p[2]] == where[p[3]] {
			t.Fatalf("dengesiz: %+v", got.Teams)
		}
	}
	first := got.Teams[0].ID
	if n := e.count(t, `SELECT count(*) FROM event_teams WHERE event_id = $1`, ev.ID.PG()); n != 2 {
		t.Fatalf("takım satırı = %d", n)
	}

	// By hand: names, someone left out; the first team keeps its ID.
	got, err = e.teams.Replace(ctx, r, []teams.Input{{Name: " Kırmızılar ", Members: []id.ID{p[0], p[2]}}, {Members: []id.ID{p[1]}}})
	must(t, err)
	if got.Teams[0].ID != first || got.Teams[0].Name != "Kırmızılar" || got.Teams[1].Name != "" ||
		!slices.Equal(got.Teams[0].Members, sortedIDs(p[0], p[2])) || !slices.Equal(got.Teams[1].Members, []id.ID{p[1]}) {
		t.Fatalf("elle: %+v", got.Teams)
	}
	// Generating again keeps the names by position.
	got, err = e.teams.Generate(ctx, r, nil)
	must(t, err)
	if got.Teams[0].ID != first || got.Teams[0].Name != "Kırmızılar" || len(placed(t, got)) != 4 {
		t.Fatalf("yeniden kurulan: %+v", got.Teams)
	}
	// A person who answers "not going" drops out of their team.
	_, err = e.events.SetRSVP(ctx, p[0], tf, ev.ID, event.RSVPNotGoing)
	must(t, err)
	got, err = e.teams.Get(ctx, r)
	must(t, err)
	if where := placed(t, got); len(where) != 3 {
		t.Fatalf("gelmeyen hâlâ takımda: %+v", got.Teams)
	}
	// Fewer teams: the rest go; an empty list clears them.
	got, err = e.teams.Replace(ctx, r, []teams.Input{{Members: []id.ID{p[1], p[2]}}})
	must(t, err)
	if len(got.Teams) != 1 || got.Teams[0].ID != first || got.Teams[0].Name != "" {
		t.Fatalf("tek takım: %+v", got.Teams)
	}
	got, err = e.teams.Replace(ctx, r, nil)
	must(t, err)
	if len(got.Teams) != 0 || e.count(t, `SELECT count(*) FROM event_team_members WHERE event_id = $1`, ev.ID.PG()) != 0 {
		t.Fatalf("temizleme: %+v", got.Teams)
	}

	if n := e.changes(t, ev.ID, key); n != 9 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'mode' = 'balanced' AND metadata ->> 'teams' = '2'`,
		teams.AuditGenerated, ev.ID.PG()); n != 6 {
		t.Fatalf("kurma denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, teams.AuditReplaced, ev.ID.PG()); n != 3 {
		t.Fatalf("elle kurma denetim kaydı = %d", n)
	}
	// No person, name or level reaches the audit record or the outbox.
	for _, u := range append(p, maybe) {
		if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action IN ($3, $4) AND metadata::text ~ $2`,
			ev.ID.PG(), u.String(), teams.AuditGenerated, teams.AuditReplaced); n != 0 {
			t.Fatalf("denetim kaydında kişi: %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM domain_events WHERE subject_id = $1 AND type = $3 AND payload::text ~ $2`,
			ev.ID.PG(), u.String(), component.EventChanged); n != 0 {
			t.Fatalf("olayda kişi: %d", n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND metadata::text ~ '(Kırmızı|level)'`, ev.ID.PG()); n != 0 {
		t.Fatalf("denetim kaydında ad ya da seviye: %d", n)
	}
}

func sortedIDs(ids ...id.ID) []id.ID {
	// Members are read in the order they were written, then by ID; the
	// same write gives them one time.
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b id.ID) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// Pairs (dive buddies): the one left over joins a pair; levels are ignored
// outside balanced mode. Two teams of at most two: the rest sit out.
func TestTeamsSizes(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	dive := e.newEvent(t, tf, admin, diveType)
	divers := e.going(t, tf, dive.ID, 5)
	got, err := e.teams.Generate(ctx, ref(admin, tf, dive.ID, "buddies"), []teams.Level{{User: divers[0], Level: 5}})
	must(t, err)
	if got.Mode != teams.ModeRandom || got.TeamSize != 2 || got.TeamCount != 0 || len(placed(t, got)) != 5 || !slices.Equal(sizes(got), []int{2, 3}) {
		t.Fatalf("ikililer: %+v", got)
	}

	bench := e.newEvent(t, tf, admin, benchType)
	players := e.going(t, tf, bench.ID, 5)
	r := ref(admin, tf, bench.ID, "teams")
	got, err = e.teams.Generate(ctx, r, nil)
	must(t, err)
	if len(placed(t, got)) != 4 || !slices.Equal(sizes(got), []int{2, 2}) {
		t.Fatalf("yedekli takımlar: %+v", got)
	}
	// By hand the capacity and the number of teams hold too.
	_, err = e.teams.Replace(ctx, r, []teams.Input{{Members: players[:3]}})
	wantField(t, "kapasiteden çok", err, "teams")
	_, err = e.teams.Replace(ctx, r, []teams.Input{{}, {}, {}})
	wantField(t, "sayıdan çok takım", err, "teams")
	if _, err := e.teams.Replace(ctx, r, []teams.Input{{Members: players[:2]}, {Members: players[2:4]}}); err != nil {
		t.Fatalf("sınırda: %v", err)
	}
}

// Refusals: manual mode, too few people, someone who is not a participant,
// bad names, levels and duplicates. The database bounds the values too.
func TestTeamsRefusals(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	manual := e.newEvent(t, tf, admin, sportType, component.Selection{Key: "teams", Choices: map[string]string{"mode": teams.ModeManual}})
	p := e.going(t, tf, manual.ID, 2)
	r := ref(admin, tf, manual.ID, "teams")
	_, err := e.teams.Generate(ctx, r, nil)
	wantErr(t, "elle modda kurma", err, apperr.ErrTeamsManual)
	got, err := e.teams.Replace(ctx, r, []teams.Input{{Members: p[:1]}, {Members: p[1:]}})
	must(t, err)
	if got.Mode != teams.ModeManual || len(placed(t, got)) != 2 {
		t.Fatalf("elle mod: %+v", got)
	}

	few := e.newEvent(t, tf, admin, sportType)
	e.going(t, tf, few.ID, 1)
	_, err = e.teams.Generate(ctx, ref(admin, tf, few.ID, "teams"), nil)
	wantErr(t, "tek katılımcı", err, apperr.ErrTeamsTooFew)

	outsider := e.member(t, tf, authz.Member)
	_, err = e.teams.Replace(ctx, r, []teams.Input{{Members: []id.ID{p[0], outsider}}})
	wantErr(t, "katılımcı olmayan", err, apperr.ErrTeamsNotParticipant)
	_, err = e.teams.Replace(ctx, r, []teams.Input{{Members: []id.ID{id.New()}}})
	wantErr(t, "üye olmayan", err, apperr.ErrTeamsNotParticipant)

	for name, in := range map[string][]teams.Input{
		"uzun ad":           {{Name: strings.Repeat("ç", teams.NameMax+1)}},
		"denetim karakteri": {{Name: "Mavi\x00"}},
		"bozuk UTF-8":       {{Name: "Ma\xffvi"}},
		"iki takımda":       {{Members: p[:1]}, {Members: p[:1]}},
		"aynı takımda iki":  {{Members: []id.ID{p[0], p[0]}}},
		"boş kimlik":        {{Members: []id.ID{{}}}},
		"çok takım":         make([]teams.Input, teams.MaxTeams+1),
	} {
		_, err := e.teams.Replace(ctx, r, in)
		wantField(t, name, err, "teams")
	}
	for name, levels := range map[string][]teams.Level{
		"düşük seviye": {{User: p[0], Level: 0}},
		"yüksek":       {{User: p[0], Level: 6}},
		"iki kez":      {{User: p[0], Level: 2}, {User: p[0], Level: 3}},
		"boş kimlik":   {{Level: 2}},
	} {
		_, err := e.teams.Generate(ctx, r, levels)
		wantField(t, name, err, "levels")
	}
	// The longest name is allowed.
	if _, err := e.teams.Replace(ctx, r, []teams.Input{{Name: strings.Repeat("ç", teams.NameMax)}}); err != nil {
		t.Fatalf("en uzun ad: %v", err)
	}

	for name, tc := range map[string]struct{ sql, constraint string }{
		"boş ad":        {`INSERT INTO event_teams (id, event_id, component_key, position, name) VALUES (gen_random_uuid(), $1, 'teams', 9, '')`, "event_teams_name_len"},
		"sıfırıncı":     {`INSERT INTO event_teams (id, event_id, component_key, position) VALUES (gen_random_uuid(), $1, 'teams', 0)`, "event_teams_position"},
		"aynı sıra":     {`INSERT INTO event_teams (id, event_id, component_key, position) VALUES (gen_random_uuid(), $1, 'teams', 1)`, "event_teams_position_uniq"},
		"yabancı takım": {`INSERT INTO event_team_members (event_id, component_key, team_id, user_id) VALUES ($1, 'teams', gen_random_uuid(), gen_random_uuid())`, "event_team_members_team"},
	} {
		_, err := e.pool.Exec(ctx, tc.sql, manual.ID.PG())
		if c := constraintOf(err); c != tc.constraint {
			t.Errorf("%s: %q (%v)", name, c, err)
		}
	}
	// A member row cannot point to another event's team.
	other := e.newEvent(t, tf, admin, sportType)
	otherP := e.going(t, tf, other.ID, 2)
	got, err = e.teams.Generate(ctx, ref(admin, tf, other.ID, "teams"), nil)
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_team_members (event_id, component_key, team_id, user_id) VALUES ($1, 'teams', $2, $3)`,
		manual.ID.PG(), got.Teams[0].ID.PG(), otherP[0].PG())
	if c := constraintOf(err); c != "event_team_members_team" {
		t.Fatalf("başka etkinliğin takımı: %q (%v)", c, err)
	}
	// core_app cannot move a team or a member.
	if _, err := e.pool.Exec(ctx, `UPDATE event_teams SET event_id = $1`, id.New().PG()); err == nil {
		t.Fatal("takım başka etkinliğe taşındı")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE event_team_members SET user_id = $1`, id.New().PG()); err == nil {
		t.Fatal("takım üyesi değiştirildi")
	}
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on.
// Writes outside the type version are refused by the service and the
// trigger.
func TestTeamsOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	// "Genel" has an optional toolbox (issue #260): a type without any
	// component stands in for it.
	e.addType(t, "test_bare", `{}`)
	general := e.newEvent(t, tf, admin, "test_bare")
	dive := e.newEvent(t, tf, admin, diveType)
	off := e.newEvent(t, tf, admin, optType)
	extra := e.newEvent(t, tf, admin, optType, component.Selection{Key: "teams", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "teams"),
		"tanımda olmayan anahtar": ref(admin, tf, dive.ID, "teams"),
	} {
		_, err := e.teams.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.teams.Generate(ctx, r, nil)
		wantErr(t, name+" kurma", err, apperr.ErrComponentNotFound)
		_, err = e.teams.Replace(ctx, r, nil)
		wantErr(t, name+" elle", err, apperr.ErrComponentNotFound)
	}
	_, err := e.teams.Get(ctx, ref(admin, tf, off.ID, "teams"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.teams.Replace(ctx, ref(admin, tf, off.ID, "teams"), nil)
	wantErr(t, "kapalı bileşene yazma", err, apperr.ErrComponentDisabled)
	e.going(t, tf, extra.ID, 2)
	if _, err := e.teams.Generate(ctx, ref(admin, tf, extra.ID, "teams"), nil); err != nil {
		t.Fatalf("açılan bileşen: %v", err)
	}

	// The type's next version drops the teams: the old event keeps them,
	// an event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, diveType, diveV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, diveType, diveV2)
	newer := e.newEvent(t, tf, admin, diveType)
	if newer.TypeVersion != 2 || dive.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", dive.TypeVersion, newer.TypeVersion)
	}
	e.going(t, tf, newer.ID, 2)
	_, err = e.teams.Generate(ctx, ref(admin, tf, newer.ID, "buddies"), nil)
	wantErr(t, "yeni sürümde olmayan takımlar", err, apperr.ErrComponentNotFound)
	e.going(t, tf, dive.ID, 2)
	if got, err := e.teams.Generate(ctx, ref(admin, tf, dive.ID, "buddies"), nil); err != nil || len(got.Teams) != 1 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// The trigger refuses writes that go around the service.
	for name, tc := range map[string]struct {
		ev  id.ID
		key string
	}{
		"bileşensiz tür": {general.ID, "teams"},
		"yeni sürüm":     {newer.ID, "buddies"},
		"kapalı bileşen": {off.ID, "teams"},
	} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_teams (id, event_id, component_key, position) VALUES (gen_random_uuid(), $1, $2, 1)`, tc.ev.PG(), tc.key)
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

// Who may do what for every role; IDOR; a cancelled or frozen event takes
// nothing, and reads stay open.
func TestTeamsPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, sportType)
	const key = "teams"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	// A member's own event (members may not create events by default: the
	// organizer is written directly).
	own := e.newEvent(t, tf, admin, sportType)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())
	e.going(t, tf, ev.ID, 2)
	e.going(t, tf, own.ID, 2)

	for _, tc := range []struct {
		name string
		user id.ID
		ev   event.Event
		want error
	}{
		{"yönetici", admin, ev, nil},
		{"moderatör", moderator, ev, nil},
		{"kendi etkinliğinin organizatörü", organizer, own, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev, apperr.ErrPermissionDenied},
		{"üye", member, ev, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev, apperr.ErrTayfaNotFound},
	} {
		denied := func(what string, err error) {
			t.Helper()
			if tc.want == nil {
				must(t, err)
			} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
				t.Errorf("%s %s: %v, %v bekleniyordu", tc.name, what, err, tc.want)
			}
		}
		r := ref(tc.user, tf, tc.ev.ID, key)
		_, err := e.teams.Generate(ctx, r, nil)
		denied("kurma", err)
		_, err = e.teams.Replace(ctx, r, nil)
		denied("elle", err)

		got, err := e.teams.Get(ctx, r)
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		// A mute does not hide the teams.
		must(t, err)
		if got.CallerCanEdit != (tc.want == nil) {
			t.Errorf("%s: caller_can_edit = %v", tc.name, got.CallerCanEdit)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err := e.teams.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.teams.Generate(ctx, ref(admin2, tf2, ev.ID, key), nil)
	wantErr(t, "başka tayfa kurma", err, apperr.ErrEventNotFound)

	// After the event the teams can still be corrected, until the write
	// window closes.
	e.clock.set(ev.EndsAt.Add(time.Hour))
	_, err = e.teams.Generate(ctx, ref(admin, tf, ev.ID, key), nil)
	must(t, err)
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.teams.Generate(ctx, ref(admin, tf, ev.ID, key), nil)
	wantErr(t, "donmuş kurma", err, apperr.ErrComponentFrozen)
	_, err = e.teams.Replace(ctx, ref(admin, tf, ev.ID, key), nil)
	wantErr(t, "donmuş elle", err, apperr.ErrComponentFrozen)
	if got, err := e.teams.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || got.CallerCanEdit || len(got.Teams) != 2 {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, sportType)
	e.going(t, tf, cancelled.ID, 2)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.teams.Generate(ctx, ref(admin, tf, cancelled.ID, key), nil)
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.teams.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinlikte yazma izni: %+v %v", got, err)
	}
}

// A person's places are exported (ExportTeamMemberships): only theirs, with
// the team's ID and position, never teammates or the team's name.
func TestTeamsExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, sportType)
	p := e.going(t, tf, ev.ID, 2)
	got, err := e.teams.Replace(ctx, ref(admin, tf, ev.ID, "teams"), []teams.Input{{Name: "Maviler", Members: p[:1]}, {Members: p[1:]}})
	must(t, err)
	rows, err := store.New(e.pool).ExportTeamMemberships(ctx, store.ExportTeamMembershipsParams{UserID: p[0].PG(), After: pgtype.UUID{Bytes: [16]byte{}, Valid: true}, N: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("dışa aktarma: %d satır", len(rows))
	}
	data := string(rows[0].Data)
	if !strings.Contains(data, got.Teams[0].ID.String()) || !strings.Contains(data, `"team_position": 1`) ||
		strings.Contains(data, p[1].String()) || strings.Contains(data, "Maviler") {
		t.Fatalf("dışa aktarılan: %s", data)
	}
}
