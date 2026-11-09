package poll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type env struct {
	svc    *Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	shift  time.Duration
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	e := &env{pool: pool}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	if e.svc, err = New(Config{Pool: pool, Now: now}); err != nil {
		t.Fatal(err)
	}
	if e.tayfas, err = tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func field(t *testing.T, what string, err error, want string) {
	t.Helper()
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Field != want {
		t.Errorf("%s: %v, %q alanı bekleniyordu", what, err, want)
	}
}

// crew is a tayfa with one member of each kind.
type crew struct {
	tayfa                              id.ID
	admin, admin2, mod, mod2, member   id.ID
	member2, muted, pending, nonMember id.ID
}

func (e *env) crew(t *testing.T) crew {
	t.Helper()
	ctx := context.Background()
	c := crew{admin: id.New(), nonMember: id.New()}
	v, err := e.tayfas.Create(ctx, c.admin, tayfa.CreateInput{Name: "Kamp Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	c.tayfa = v.ID
	c.admin2 = e.member(t, c, authz.Admin)
	c.mod = e.member(t, c, authz.Moderator)
	c.mod2 = e.member(t, c, authz.Moderator)
	c.member = e.member(t, c, authz.Member)
	c.member2 = e.member(t, c, authz.Member)
	c.muted = e.member(t, c, authz.Member)
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.muted.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	c.pending = id.New()
	e.join(t, c.tayfa, c.pending, authz.StatusPending)
	return c
}

func (e *env) as(t *testing.T, actor id.ID, fn func(q *store.Queries) error) {
	t.Helper()
	must(t, txn.WithActor(context.Background(), e.pool, actor, func(_ pgx.Tx, q *store.Queries) error { return fn(q) }))
}

func (e *env) join(t *testing.T, tayfa, user id.ID, st authz.Status) {
	t.Helper()
	joined := pgtype.Timestamptz{}
	if st == authz.StatusActive {
		joined = ts(time.Now())
	}
	e.as(t, user, func(q *store.Queries) error {
		return q.CreateMembership(context.Background(), store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: user.PG(), Role: "member", Status: string(st), JoinedVia: "code", JoinedAt: joined,
		})
	})
}

func (e *env) member(t *testing.T, c crew, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, c.tayfa, u, authz.StatusActive)
	e.setRole(t, c, u, role)
	return u
}

func (e *env) setRole(t *testing.T, c crew, u id.ID, role authz.Role) {
	t.Helper()
	if role == authz.Member {
		return
	}
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.SetMembershipRole(context.Background(), store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), Role: role.String()})
		return err
	})
}

// event seeds an online event in tayfa.
func (e *env) event(t *testing.T, tayfa id.ID) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, 'camp', 'online', 'Kamp', now() + interval '1 day', now() + interval '2 days', 'https://example.com')`, ev.PG(), tayfa.PG())
	return ev
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	must(t, err)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e *env) open(t *testing.T, c crew, by id.ID, in CreateInput) View {
	t.Helper()
	if in.Question == "" {
		in.Question = "Kamp hangi hafta sonu?"
	}
	if in.Options == nil {
		in.Options = []string{"Bu hafta", "Gelecek hafta", "Ay sonu"}
	}
	v, err := e.svc.Create(context.Background(), by, c.tayfa, in)
	must(t, err)
	return v
}

func optionIDs(v View) []string {
	out := make([]string, 0, len(v.Options))
	for _, o := range v.Options {
		out = append(out, o.ID.String())
	}
	return out
}

func counts(v View) []int64 {
	out := make([]int64, 0, len(v.Options))
	for _, o := range v.Options {
		out = append(out, o.VoteCount)
	}
	return out
}

func TestCreateWhoMayCreate(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"yönetici", c.admin, nil},
		{"moderatör", c.mod, nil},
		{"üye", c.member, nil},
		{"susturulmuş üye", c.muted, apperr.ErrMuted},
		{"onay bekleyen", c.pending, apperr.ErrTayfaNotFound},
		{"üye değil", c.nonMember, apperr.ErrTayfaNotFound},
	} {
		_, err := e.svc.Create(ctx, tc.user, c.tayfa, CreateInput{Question: "Soru?", Options: []string{"A", "B"}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	if n := e.count(t, "SELECT count(*) FROM polls"); n != 3 {
		t.Fatalf("anket sayısı = %d", n)
	}
}

func TestCreate(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	ev := e.event(t, c.tayfa)
	closes := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	v, err := e.svc.Create(ctx, c.member, c.tayfa, CreateInput{
		Question: "  Gizli Soru?  ", Options: []string{" Bu hafta ", "Gelecek hafta"}, Anonymous: true, MultipleChoice: true,
		ClosesAt: closes, EventID: ev.String(),
	})
	must(t, err)
	if v.Question != "Gizli Soru?" || !v.Anonymous || !v.MultipleChoice || v.EventID != ev || v.CreatedBy != c.member ||
		!v.ClosesAt.Equal(closes) || v.Closed || !v.ClosedAt.IsZero() || v.VoterCount != 0 || len(v.OwnOptionIDs) != 0 {
		t.Fatalf("görünüm = %+v", v)
	}
	if len(v.Options) != 2 || v.Options[0].Text != "Bu hafta" || v.Options[0].Position != 0 || v.Options[1].Position != 1 {
		t.Fatalf("seçenekler = %+v", v.Options)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'poll.created' AND target_id = $1 AND actor_id = $2", v.ID.PG(), c.member.PG()); n != 1 {
		t.Fatalf("denetim = %d", n)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'poll.created' AND tayfa_id = $1 AND subject_id = $2", c.tayfa.PG(), v.ID.PG()).Scan(&payload))
	if !strings.Contains(payload, ev.String()) || strings.Contains(payload, "Gizli") || strings.Contains(payload, "hafta") {
		t.Fatalf("olay = %s", payload)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Gizli%' OR metadata::text LIKE '%hafta%'"); n != 0 {
		t.Fatal("içerik denetim kaydına yazıldı")
	}
	// The event filter.
	other := e.open(t, c, c.admin, CreateInput{})
	page, _, err := e.svc.List(ctx, c.member2, c.tayfa, ev.String(), 0, "")
	if err != nil || len(page) != 1 || page[0].ID != v.ID {
		t.Fatalf("etkinliğin anketleri: %+v %v", page, err)
	}
	page, _, err = e.svc.List(ctx, c.member2, c.tayfa, "", 0, "")
	if err != nil || len(page) != 2 || page[0].ID != other.ID {
		t.Fatalf("tüm anketler: %+v %v", page, err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	two := []string{"A", "B"}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("Seçenek %d", i)
	}
	for name, tc := range map[string]struct {
		in    CreateInput
		field string
	}{
		"boş soru":           {CreateInput{Question: "  ", Options: two}, "question"},
		"uzun soru":          {CreateInput{Question: strings.Repeat("ş", 201), Options: two}, "question"},
		"çok satırlı soru":   {CreateInput{Question: "a\nb", Options: two}, "question"},
		"bozuk utf8":         {CreateInput{Question: "So\xffru", Options: two}, "question"},
		"tek seçenek":        {CreateInput{Question: "Soru", Options: []string{"A"}}, "options"},
		"on bir seçenek":     {CreateInput{Question: "Soru", Options: eleven}, "options"},
		"boş seçenek":        {CreateInput{Question: "Soru", Options: []string{"A", " "}}, "options"},
		"uzun seçenek":       {CreateInput{Question: "Soru", Options: []string{"A", strings.Repeat("ğ", 101)}}, "options"},
		"aynı seçenek":       {CreateInput{Question: "Soru", Options: []string{"İzmir", "izmir"}}, "options"},
		"geçmiş kapanma":     {CreateInput{Question: "Soru", Options: two, ClosesAt: time.Now().Add(-time.Minute)}, "close_time"},
		"çok uzak kapanma":   {CreateInput{Question: "Soru", Options: two, ClosesAt: time.Now().Add(MaxCloseAhead + time.Hour)}, "close_time"},
		"bozuk etkinlik":     {CreateInput{Question: "Soru", Options: two, EventID: "abc"}, "event_id"},
		"kontrol karakteri":  {CreateInput{Question: "Soru", Options: []string{"A\x00", "B"}}, "options"},
		"satır sonlu seçenk": {CreateInput{Question: "Soru", Options: []string{"A\nB", "B"}}, "options"},
	} {
		_, err := e.svc.Create(ctx, c.admin, c.tayfa, tc.in)
		field(t, name, err, tc.field)
	}
	// Limits are inclusive.
	ten := eleven[:10]
	ten[9] = strings.Repeat("ğ", 100)
	_, err := e.svc.Create(ctx, c.admin, c.tayfa, CreateInput{Question: strings.Repeat("ş", 200), Options: ten})
	must(t, err)

	// The event must be a visible event of the same tayfa.
	other := e.crew(t)
	foreign := e.event(t, other.tayfa)
	deleted := e.event(t, c.tayfa)
	e.exec(t, "UPDATE events SET deleted_at = now() WHERE id = $1", deleted.PG())
	for name, ev := range map[string]id.ID{"başka tayfanın": foreign, "silinmiş": deleted, "bilinmeyen": id.New()} {
		_, err := e.svc.Create(ctx, c.admin, c.tayfa, CreateInput{Question: "Soru", Options: two, EventID: ev.String()})
		is(t, name+" etkinlik", err, apperr.ErrEventNotFound)
	}
	if n := e.count(t, "SELECT count(*) FROM polls WHERE event_id IS NOT NULL"); n != 0 {
		t.Fatal("geçersiz etkinliğe anket bağlandı")
	}
}

func TestVoteSingleChoice(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	p := e.open(t, c, c.member, CreateInput{})
	opts := optionIDs(p)

	v, err := e.svc.Vote(ctx, c.member2, c.tayfa, p.ID, []string{opts[0]})
	must(t, err)
	if !slices.Equal(counts(v), []int64{1, 0, 0}) || v.VoterCount != 1 || len(v.OwnOptionIDs) != 1 || v.OwnOptionIDs[0].String() != opts[0] {
		t.Fatalf("oy: %+v", v)
	}
	// Changing the vote deletes the old one.
	v, err = e.svc.Vote(ctx, c.member2, c.tayfa, p.ID, []string{opts[2]})
	must(t, err)
	if !slices.Equal(counts(v), []int64{0, 0, 1}) || v.VoterCount != 1 || v.OwnOptionIDs[0].String() != opts[2] {
		t.Fatalf("oy değiştirme: %+v", v)
	}
	// The same vote again records nothing.
	_, err = e.svc.Vote(ctx, c.member2, c.tayfa, p.ID, []string{opts[2]})
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'poll.voted' AND subject_id = $1", p.ID.PG()); n != 2 {
		t.Fatalf("oy olayı = %d", n)
	}
	// Votes are not audited.
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action <> 'poll.created'", p.ID.PG()); n != 0 {
		t.Fatalf("oy denetime yazıldı: %d", n)
	}
	// Someone else's view shows the totals and no own vote.
	g, err := e.svc.Get(ctx, c.admin, c.tayfa, p.ID)
	if err != nil || len(g.OwnOptionIDs) != 0 || !slices.Equal(counts(g), []int64{0, 0, 1}) {
		t.Fatalf("başkasının görünümü: %+v %v", g, err)
	}

	other := e.open(t, c, c.member, CreateInput{})
	for name, ids := range map[string][]string{
		"iki seçenek":          {opts[0], opts[1]},
		"başka anketin":        {optionIDs(other)[0]},
		"bilinmeyen":           {id.New().String()},
		"boş":                  {},
		"tekrar":               {opts[0], opts[0]},
		"bozuk":                {"' OR 1=1 --"},
		"karışık":              {opts[0], "x"},
		"başkasının seçenekli": {opts[0], optionIDs(other)[1]},
	} {
		_, err := e.svc.Vote(ctx, c.member, c.tayfa, p.ID, ids)
		field(t, name, err, "option_ids")
	}
	if n := e.count(t, "SELECT count(*) FROM poll_votes WHERE user_id = $1", c.member.PG()); n != 0 {
		t.Fatal("geçersiz oy yazıldı")
	}

	// Who may vote.
	_, err = e.svc.Vote(ctx, c.muted, c.tayfa, p.ID, []string{opts[0]})
	is(t, "susturulmuş", err, apperr.ErrMuted)
	_, err = e.svc.Withdraw(ctx, c.muted, c.tayfa, p.ID)
	is(t, "susturulmuş geri çekme", err, apperr.ErrMuted)
	_, err = e.svc.Get(ctx, c.muted, c.tayfa, p.ID)
	must(t, err)
	for _, u := range []id.ID{c.pending, c.nonMember} {
		_, err = e.svc.Vote(ctx, u, c.tayfa, p.ID, []string{opts[0]})
		is(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	}
	_, err = e.svc.Vote(ctx, c.member, c.tayfa, id.New(), []string{opts[0]})
	is(t, "bilinmeyen anket", err, apperr.ErrPollNotFound)

	// Withdrawing.
	v, err = e.svc.Withdraw(ctx, c.member2, c.tayfa, p.ID)
	if err != nil || v.VoterCount != 0 || len(v.OwnOptionIDs) != 0 {
		t.Fatalf("geri çekme: %+v %v", v, err)
	}
	_, err = e.svc.Withdraw(ctx, c.member2, c.tayfa, p.ID)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'poll.voted' AND subject_id = $1 AND payload->>'withdrawn' = 'true'", p.ID.PG()); n != 1 {
		t.Fatalf("geri çekme olayı = %d", n)
	}
}

func TestVoteMultipleChoice(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	p := e.open(t, c, c.mod, CreateInput{MultipleChoice: true})
	opts := optionIDs(p)
	_, err := e.svc.Vote(ctx, c.member, c.tayfa, p.ID, []string{opts[2], opts[0]})
	must(t, err)
	v, err := e.svc.Vote(ctx, c.member2, c.tayfa, p.ID, []string{opts[0]})
	must(t, err)
	if !slices.Equal(counts(v), []int64{2, 0, 1}) || v.VoterCount != 2 {
		t.Fatalf("sayım: %+v", v)
	}
	v, err = e.svc.Vote(ctx, c.member, c.tayfa, p.ID, []string{opts[1], opts[2]})
	must(t, err)
	if !slices.Equal(counts(v), []int64{1, 1, 1}) || v.VoterCount != 2 || len(v.OwnOptionIDs) != 2 ||
		v.OwnOptionIDs[0].String() != opts[1] || v.OwnOptionIDs[1].String() != opts[2] {
		t.Fatalf("değişen çoklu oy: %+v", v)
	}
	// The open poll's event names the options; voters are listed.
	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'poll.voted' AND subject_id = $1 ORDER BY id DESC LIMIT 1", p.ID.PG()).Scan(&payload))
	if !strings.Contains(payload, opts[1]) {
		t.Fatalf("açık anketin olayı = %s", payload)
	}
	voters, next, err := e.svc.ListVoters(ctx, c.muted, c.tayfa, p.ID, opts[2], 1, "")
	must(t, err)
	if len(voters) != 1 || next == "" {
		t.Fatalf("oy verenler: %+v %q", voters, next)
	}
	more, next2, err := e.svc.ListVoters(ctx, c.muted, c.tayfa, p.ID, opts[2], 1, next)
	must(t, err)
	if len(more) != 0 || next2 != "" {
		t.Fatalf("ikinci sayfa: %+v", more)
	}
	if voters[0].UserID != c.member {
		t.Fatalf("oy veren = %v", voters[0].UserID)
	}
	_, _, err = e.svc.ListVoters(ctx, c.member, c.tayfa, p.ID, opts[0], 0, next)
	field(t, "başka seçeneğin token'ı", err, "page_token")
	_, _, err = e.svc.ListVoters(ctx, c.member, c.tayfa, p.ID, id.New().String(), 0, "")
	field(t, "bilinmeyen seçenek", err, "option_id")
}

func TestCloseAndDelete(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	byMember := e.open(t, c, c.member, CreateInput{})
	byMod := e.open(t, c, c.mod, CreateInput{})
	byAdmin := e.open(t, c, c.admin, CreateInput{})

	for _, tc := range []struct {
		name string
		user id.ID
		p    View
		want error
	}{
		{"başka üye", c.member2, byMember, apperr.ErrPermissionDenied},
		{"susturulmuş başkasının", c.muted, byMember, apperr.ErrPermissionDenied},
		{"moderatör moderatörün", c.mod2, byMod, apperr.ErrRoleHierarchy},
		{"moderatör yöneticinin", c.mod, byAdmin, apperr.ErrRoleHierarchy},
		{"yönetici yöneticinin", c.admin2, byAdmin, apperr.ErrRoleHierarchy},
		{"bilinmeyen", c.admin, View{ID: id.New()}, apperr.ErrPollNotFound},
	} {
		_, err := e.svc.Close(ctx, tc.user, c.tayfa, tc.p.ID)
		if !errors.Is(err, tc.want) {
			t.Errorf("kapatma %s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		if err := e.svc.Delete(ctx, tc.user, c.tayfa, tc.p.ID); !errors.Is(err, tc.want) {
			t.Errorf("silme %s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}

	// The creator closes their own poll; closed polls take no votes.
	opts := optionIDs(byMember)
	_, err := e.svc.Vote(ctx, c.member2, c.tayfa, byMember.ID, []string{opts[1]})
	must(t, err)
	v, err := e.svc.Close(ctx, c.member, c.tayfa, byMember.ID)
	if err != nil || !v.Closed || v.ClosedAt.IsZero() || v.Options[1].VoteCount != 1 {
		t.Fatalf("kapatma: %+v %v", v, err)
	}
	_, err = e.svc.Close(ctx, c.member, c.tayfa, byMember.ID)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'poll.closed' AND target_id = $1", byMember.ID.PG()); n != 1 {
		t.Fatalf("kapatma kaydı = %d", n)
	}
	_, err = e.svc.Vote(ctx, c.member, c.tayfa, byMember.ID, []string{opts[0]})
	is(t, "kapalı ankete oy", err, apperr.ErrPollClosed)
	_, err = e.svc.Withdraw(ctx, c.member2, c.tayfa, byMember.ID)
	is(t, "kapalı ankette geri çekme", err, apperr.ErrPollClosed)

	// A moderator closes a member's poll; an admin a moderator's.
	byMember2 := e.open(t, c, c.member2, CreateInput{})
	_, err = e.svc.Close(ctx, c.mod, c.tayfa, byMember2.ID)
	must(t, err)
	_, err = e.svc.Close(ctx, c.admin, c.tayfa, byMod.ID)
	must(t, err)

	// The close time.
	timed := e.open(t, c, c.member, CreateInput{ClosesAt: time.Now().Add(time.Hour)})
	_, err = e.svc.Vote(ctx, c.member2, c.tayfa, timed.ID, []string{optionIDs(timed)[0]})
	must(t, err)
	e.shift = time.Hour + time.Minute
	_, err = e.svc.Vote(ctx, c.member, c.tayfa, timed.ID, []string{optionIDs(timed)[0]})
	is(t, "süresi geçmiş", err, apperr.ErrPollClosed)
	g, err := e.svc.Get(ctx, c.member, c.tayfa, timed.ID)
	if err != nil || !g.Closed || !g.ClosedAt.IsZero() {
		t.Fatalf("süresi geçmiş görünüm: %+v %v", g, err)
	}
	e.shift = 0

	// Deleting removes the poll and its votes at once.
	must(t, e.svc.Delete(ctx, c.member, c.tayfa, byMember.ID))
	if n := e.count(t, "SELECT count(*) FROM poll_votes WHERE poll_id = $1", byMember.ID.PG()); n != 0 {
		t.Fatal("silinen anketin oyları kaldı")
	}
	_, err = e.svc.Get(ctx, c.member, c.tayfa, byMember.ID)
	is(t, "silinen okundu", err, apperr.ErrPollNotFound)
	is(t, "iki kez silme", e.svc.Delete(ctx, c.member, c.tayfa, byMember.ID), apperr.ErrPollNotFound)
	must(t, e.svc.Delete(ctx, c.mod, c.tayfa, byMember2.ID))
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'poll.deleted'"); n != 2 {
		t.Fatalf("silme olayı = %d", n)
	}

	// A creator who left: the poll belongs to the tayfa.
	byMod2 := e.open(t, c, c.mod2, CreateInput{})
	e.as(t, c.mod2, func(q *store.Queries) error {
		_, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.mod2.PG()})
		return err
	})
	must(t, e.svc.Delete(ctx, c.mod, c.tayfa, byMod2.ID))

	// A muted creator can no longer close or delete their own poll.
	mine := e.open(t, c, c.member2, CreateInput{})
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.member2.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	_, err = e.svc.Close(ctx, c.member2, c.tayfa, mine.ID)
	is(t, "susturulmuş kendi anketini kapattı", err, apperr.ErrMuted)
	is(t, "susturulmuş kendi anketini sildi", e.svc.Delete(ctx, c.member2, c.tayfa, mine.ID), apperr.ErrMuted)
}

// callAll calls every method as user on poll p of tayfaID.
func (e *env) callAll(user, tayfaID id.ID, p View) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	opt := id.New().String()
	if len(p.Options) > 0 {
		opt = p.Options[0].ID.String()
	}
	_, out["Create"] = e.svc.Create(ctx, user, tayfaID, CreateInput{Question: "Soru", Options: []string{"A", "B"}})
	_, out["Get"] = e.svc.Get(ctx, user, tayfaID, p.ID)
	_, _, out["List"] = e.svc.List(ctx, user, tayfaID, "", 0, "")
	_, out["Vote"] = e.svc.Vote(ctx, user, tayfaID, p.ID, []string{opt})
	_, out["Withdraw"] = e.svc.Withdraw(ctx, user, tayfaID, p.ID)
	_, _, out["ListVoters"] = e.svc.ListVoters(ctx, user, tayfaID, p.ID, opt, 0, "")
	_, out["Close"] = e.svc.Close(ctx, user, tayfaID, p.ID)
	out["Delete"] = e.svc.Delete(ctx, user, tayfaID, p.ID)
	return out
}

func TestModuleDisabled(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	p := e.open(t, c, c.admin, CreateInput{})
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "polls", false))
	for method, err := range e.callAll(c.admin, c.tayfa, p) {
		is(t, method, err, apperr.ErrModuleDisabled)
	}
	for method, err := range e.callAll(c.nonMember, c.tayfa, p) {
		is(t, "üye olmayan "+method, err, apperr.ErrTayfaNotFound)
	}
	if n := e.count(t, "SELECT count(*) FROM polls WHERE deleted_at IS NULL"); n != 1 {
		t.Fatal("kapalı modülde yazıldı")
	}
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "polls", true))
	_, err := e.svc.Get(ctx, c.member, c.tayfa, p.ID)
	must(t, err)
}

func TestReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	p := e.open(t, c, c.admin, CreateInput{})
	_, err := e.pool.Exec(ctx, "UPDATE tayfas SET suspended_at = now(), suspended_reason = 'kural ihlali' WHERE id = $1", c.tayfa.PG())
	must(t, err)
	for method, err := range e.callAll(c.admin, c.tayfa, p) {
		switch method {
		case "Get", "List", "ListVoters":
			must(t, err)
		default:
			is(t, method, err, apperr.ErrTayfaReadOnly)
		}
	}
}

// TestOtherTayfasPoll: an admin of one tayfa cannot reach another tayfa's
// poll through their own tayfa's ID (IDOR).
func TestOtherTayfasPoll(t *testing.T) {
	e := setup(t)
	victim, attacker := e.crew(t), e.crew(t)
	p := e.open(t, victim, victim.member, CreateInput{})
	for method, err := range e.callAll(attacker.admin, attacker.tayfa, p) {
		switch method {
		case "Create", "List":
			must(t, err)
		default:
			is(t, method, err, apperr.ErrPollNotFound)
		}
	}
	for method, err := range e.callAll(attacker.admin, victim.tayfa, p) {
		is(t, method, err, apperr.ErrTayfaNotFound)
	}
	v, err := e.svc.Get(context.Background(), victim.member, victim.tayfa, p.ID)
	if err != nil || v.Closed || v.VoterCount != 0 {
		t.Fatalf("hedef değişti: %+v %v", v, err)
	}
}

// TestDatabaseRules checks the triggers and constraints behind the app layer
// (ADR-0011: the database refuses what the app layer should never send).
func TestDatabaseRules(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	p := e.open(t, c, c.member, CreateInput{})
	opts := p.Options
	insertVote := func(user id.ID, opt Option) error {
		_, err := e.pool.Exec(ctx, "INSERT INTO poll_votes (poll_id, option_id, user_id) VALUES ($1, $2, $3)", p.ID.PG(), opt.ID.PG(), user.PG())
		return err
	}
	code := func(what string, err error, want string) {
		t.Helper()
		if got := store.SQLState(err); got != want {
			t.Errorf("%s: %v, %s bekleniyordu", what, err, want)
		}
	}
	must(t, insertVote(c.member2, opts[0]))
	code("tek seçimli ankette ikinci oy", insertVote(c.member2, opts[1]), store.CodePollVote)
	code("üye olmayanın oyu", insertVote(c.nonMember, opts[0]), store.CodePollVote)
	code("onay bekleyenin oyu", insertVote(c.pending, opts[0]), store.CodePollVote)
	code("susturulmuşun oyu", insertVote(c.muted, opts[0]), store.CodePollVote)
	_, err := e.pool.Exec(ctx, "UPDATE poll_votes SET option_id = $1 WHERE poll_id = $2", opts[1].ID.PG(), p.ID.PG())
	code("oy güncelleme", err, store.CodePollVote)

	// Another poll's option.
	other := e.open(t, c, c.member, CreateInput{})
	_, err = e.pool.Exec(ctx, "INSERT INTO poll_votes (poll_id, option_id, user_id) VALUES ($1, $2, $3)", p.ID.PG(), other.Options[0].ID.PG(), c.admin.PG())
	code("başka anketin seçeneği", err, "23503")

	// The settings are frozen after the first vote; before it they change.
	_, err = e.pool.Exec(ctx, "UPDATE polls SET anonymous = true WHERE id = $1", p.ID.PG())
	code("oy sonrası gizlilik", err, store.CodePollFrozen)
	_, err = e.pool.Exec(ctx, "UPDATE polls SET multiple_choice = true WHERE id = $1", p.ID.PG())
	code("oy sonrası çoklu seçim", err, store.CodePollFrozen)
	_, err = e.pool.Exec(ctx, "UPDATE polls SET anonymous = true WHERE id = $1", other.ID.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, "UPDATE polls SET tayfa_id = $1 WHERE id = $2", e.crew(t).tayfa.PG(), other.ID.PG())
	code("anketin tayfası", err, store.CodePollFrozen)
	_, err = e.pool.Exec(ctx, "UPDATE poll_options SET text = 'x' WHERE poll_id = $1", p.ID.PG())
	code("seçenek değiştirme", err, store.CodePollFrozen)

	// A closed or deleted poll takes no votes.
	e.exec(t, "UPDATE polls SET closed_at = now() WHERE id = $1", p.ID.PG())
	code("kapalı ankete oy", insertVote(c.admin, opts[0]), store.CodePollVote)

	// Fewer than two options.
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	pid := id.New()
	_, err = tx.Exec(ctx, "INSERT INTO polls (id, tayfa_id, question) VALUES ($1, $2, 'Soru')", pid.PG(), c.tayfa.PG())
	must(t, err)
	_, err = tx.Exec(ctx, "INSERT INTO poll_options (id, poll_id, text, position) VALUES ($1, $2, 'Tek', 0)", id.New().PG(), pid.PG())
	must(t, err)
	code("tek seçenekli anket", tx.Commit(ctx), store.CodePollFrozen)

	// The event must be in the same tayfa (composite foreign key).
	foreign := e.event(t, e.crew(t).tayfa)
	_, err = e.pool.Exec(ctx, `WITH p AS (INSERT INTO polls (id, tayfa_id, event_id, question) VALUES ($1, $2, $3, 'Soru') RETURNING id)
		INSERT INTO poll_options (id, poll_id, text, position) SELECT $4::uuid, id, 'A', 0 FROM p UNION ALL SELECT $5::uuid, id, 'B', 1 FROM p`,
		id.New().PG(), c.tayfa.PG(), foreign.PG(), id.New().PG(), id.New().PG())
	code("başka tayfanın etkinliği", err, "23503")
}

// TestListPaging pages through polls and checks page tokens.
func TestListPaging(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	var want []id.ID
	for range 5 {
		want = append([]id.ID{e.open(t, c, c.member, CreateInput{}).ID}, want...)
	}
	var got []id.ID
	token := ""
	for range 10 {
		page, next, err := e.svc.List(ctx, c.muted, c.tayfa, "", 2, token)
		must(t, err)
		for _, p := range page {
			got = append(got, p.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sıra: %v, %v bekleniyordu", got, want)
	}
	_, next, err := e.svc.List(ctx, c.member, c.tayfa, "", 1, "")
	must(t, err)
	for _, bad := range []string{next[:len(next)-2], "!!", strings.Repeat("a", 600)} {
		_, _, err := e.svc.List(ctx, c.member, c.tayfa, "", 1, bad)
		field(t, "bozuk token", err, "page_token")
	}
	_, _, err = e.svc.List(ctx, c.member, c.tayfa, id.New().String(), 1, next)
	field(t, "başka filtrenin token'ı", err, "page_token")
	other := e.crew(t)
	_, _, err = e.svc.List(ctx, other.admin, other.tayfa, "", 1, next)
	field(t, "başka tayfanın token'ı", err, "page_token")
	_, _, err = e.svc.List(ctx, c.member, c.tayfa, "", -1, "")
	field(t, "negatif sayfa", err, "page_size")
	_, _, err = e.svc.List(ctx, c.member, c.tayfa, "x", 0, "")
	field(t, "bozuk etkinlik", err, "event_id")
}
