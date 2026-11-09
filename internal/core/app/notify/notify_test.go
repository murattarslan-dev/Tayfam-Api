package notify

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixture writes rows with triggers disabled (the test connection is a
// superuser); notify only reads them.
type fixture struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	return &fixture{pool: pool, q: store.New(pool)}
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := f.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (f *fixture) tayfa(t *testing.T, code string) id.ID {
	t.Helper()
	tf := id.New()
	f.exec(t, `INSERT INTO tayfas (id, name, name_normalized, join_code, join_policy, created_by)
		VALUES ($1, 'Tayfa', 'tayfa', $2, 'open', $3)`, tf.PG(), code, id.New().PG())
	return tf
}

func (f *fixture) member(t *testing.T, tf id.ID, role, status string) id.ID {
	t.Helper()
	u := id.New()
	f.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
		VALUES ($1, $2, $3, $4, 'code', CASE WHEN $4 = 'active' THEN now() END)`, tf.PG(), u.PG(), role, status)
	return u
}

func (f *fixture) event(t *testing.T, tf id.ID) id.ID {
	t.Helper()
	ev := id.New()
	start := time.Now().Add(24 * time.Hour)
	f.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, 'general', 'online', 'Etkinlik', $3, $4, 'https://example.org/x')`, ev.PG(), tf.PG(), start, start.Add(time.Hour))
	return ev
}

func (f *fixture) rsvp(t *testing.T, ev, u id.ID, status string) {
	t.Helper()
	f.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, $3)`, ev.PG(), u.PG(), status)
}

func sorted(us ...id.ID) []id.ID {
	out := slices.Clone(us)
	slices.SortFunc(out, func(a, b id.ID) int { return slices.Compare(a[:], b[:]) })
	return out
}

// Recipients are active members only, never the actor; role and answer
// filters narrow them further (issue #64).
func TestRecipients(t *testing.T) {
	f := setup(t)
	tf := f.tayfa(t, "NTFYAB23")
	otherTayfa := f.tayfa(t, "NTFYCD45")
	admin := f.member(t, tf, "admin", "active")
	mod := f.member(t, tf, "moderator", "active")
	member := f.member(t, tf, "member", "active")
	actor := f.member(t, tf, "member", "active")
	pending := f.member(t, tf, "member", "pending")
	banned := f.member(t, tf, "member", "banned")
	elsewhere := f.member(t, otherTayfa, "member", "active")
	left := id.New() // was a member once: no row any more

	ev := f.event(t, tf)
	f.rsvp(t, ev, member, "going")
	f.rsvp(t, ev, mod, "waitlist")
	f.rsvp(t, ev, actor, "maybe")
	f.rsvp(t, ev, admin, "not_going")
	f.rsvp(t, ev, banned, "going")
	f.rsvp(t, ev, left, "going")

	for _, tc := range []struct {
		name string
		got  func() ([]id.ID, error)
		want []id.ID
	}{
		{"üyeler", func() ([]id.ID, error) { return Members(ctx, f.q, tf, actor) }, sorted(admin, mod, member)},
		{"üyeler (sistem)", func() ([]id.ID, error) { return Members(ctx, f.q, tf, id.ID{}) }, sorted(admin, mod, member, actor)},
		{"yönetim", func() ([]id.ID, error) { return Staff(ctx, f.q, tf, actor) }, sorted(admin, mod)},
		{"yönetim, aktör moderatör", func() ([]id.ID, error) { return Staff(ctx, f.q, tf, mod) }, sorted(admin)},
		{"katılımcılar", func() ([]id.ID, error) { return Participants(ctx, f.q, tf, ev, actor) }, sorted(member, mod)},
		{"seçilenler", func() ([]id.ID, error) {
			return Only(ctx, f.q, tf, []id.ID{member, pending, banned, left, elsewhere, actor, {}}, actor)
		}, sorted(member)},
		{"seçilen yok", func() ([]id.ID, error) { return Only(ctx, f.q, tf, nil, actor) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.got()
			must(t, err)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("alıcılar = %v, beklenen %v", got, tc.want)
			}
		})
	}
}

type written struct {
	actor   pgtype.UUID
	subject pgtype.UUID
	tayfa   pgtype.UUID
	payload map[string]any
}

func (f *fixture) written(t *testing.T) []written {
	t.Helper()
	rows, err := f.pool.Query(ctx, `SELECT actor_id, subject_id, tayfa_id, payload FROM domain_events WHERE type = $1 ORDER BY id`, EventType)
	must(t, err)
	defer rows.Close()
	var out []written
	for rows.Next() {
		var w written
		var raw []byte
		must(t, rows.Scan(&w.actor, &w.subject, &w.tayfa, &raw))
		must(t, json.Unmarshal(raw, &w.payload))
		out = append(out, w)
	}
	return out
}

func (f *fixture) write(t *testing.T, r Request, to []id.ID) error {
	t.Helper()
	return pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return Write(ctx, store.New(tx), r, to) })
}

// A large audience is split over several events, each under the payload
// limit; nothing but IDs and classification is written.
func TestWriteSplitsRecipients(t *testing.T) {
	f := setup(t)
	tf, actor, target := id.New(), id.New(), id.New()
	to := make([]id.ID, 2*MaxRecipients+1)
	for i := range to {
		to[i] = id.New()
	}
	r := Request{Type: TypeAnswerReceived, Tayfa: tf, Actor: actor, TargetType: TargetAnswer, Target: target,
		Extra: map[string]string{ExtraQuestionID: id.New().String()}}
	must(t, f.write(t, r, to))

	got := f.written(t)
	if len(got) != 3 {
		t.Fatalf("%d olay", len(got))
	}
	var all []string
	for _, w := range got {
		ids := w.payload["recipient_ids"].([]any)
		if len(ids) > MaxRecipients {
			t.Fatalf("olayda %d alıcı", len(ids))
		}
		for _, v := range ids {
			all = append(all, v.(string))
		}
		if id.ID(w.actor.Bytes) != actor || id.ID(w.subject.Bytes) != target || id.ID(w.tayfa.Bytes) != tf ||
			w.payload["type"] != TypeAnswerReceived || w.payload["target_type"] != TargetAnswer || w.payload["question_id"] == nil {
			t.Fatalf("olay = %+v", w)
		}
		for k := range w.payload {
			if !slices.Contains([]string{"type", "tayfa_id", "target_type", "target_id", "recipient_ids", "question_id"}, k) {
				t.Errorf("beklenmeyen alan %q", k)
			}
		}
	}
	if len(all) != len(to) {
		t.Fatalf("%d alıcı yazıldı", len(all))
	}

	// No recipients: no event.
	must(t, f.write(t, r, nil))
	if n := len(f.written(t)); n != 3 {
		t.Fatalf("alıcısız istek olay yazdı: %d", n)
	}
	// Only known extra keys; required fields.
	for name, bad := range map[string]Request{
		"metin alanı":        {Type: TypeAnnouncement, Tayfa: tf, TargetType: TargetAnnouncement, Target: target, Extra: map[string]string{"title": "x"}},
		"türsüz":             {Tayfa: tf, TargetType: TargetAnnouncement, Target: target},
		"hedefsiz":           {Type: TypeAnnouncement, Tayfa: tf, TargetType: TargetAnnouncement},
		"tayfasız":           {Type: TypeAnnouncement, TargetType: TargetAnnouncement, Target: target},
		"tayfalı arkadaşlık": {Type: TypeFriendRequest, Tayfa: tf, TargetType: TargetUser, Target: target},
	} {
		if err := f.write(t, bad, to[:1]); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
}

// Friend notifications are not about a tayfa: no tayfa_id anywhere (issue
// #71). The invitation names its tayfa.
func TestWriteWithoutTayfa(t *testing.T) {
	f := setup(t)
	actor, friend := id.New(), id.New()
	must(t, f.write(t, Request{Type: TypeFriendRequest, Actor: actor, TargetType: TargetUser, Target: actor}, []id.ID{friend}))
	got := f.written(t)
	if len(got) != 1 || got[0].tayfa.Valid || got[0].payload["tayfa_id"] != nil || got[0].payload["target_type"] != TargetUser {
		t.Fatalf("olay = %+v", got)
	}
	tf := id.New()
	must(t, f.write(t, Request{Type: TypeTayfaInvitation, Tayfa: tf, Actor: actor, TargetType: TargetTayfa, Target: tf}, []id.ID{friend}))
	if got := f.written(t); len(got) != 2 || id.ID(got[1].tayfa.Bytes) != tf {
		t.Fatalf("davet = %+v", got)
	}
	if err := f.write(t, Request{Type: TypeTayfaInvitation, Actor: actor, TargetType: TargetTayfa, Target: tf}, []id.ID{friend}); err == nil {
		t.Fatal("tayfasız davet kabul edildi")
	}
}
