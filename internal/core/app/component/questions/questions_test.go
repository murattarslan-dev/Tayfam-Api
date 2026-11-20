package questions_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/questions"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. talkType has a questions component (key "questions") that
// allows anonymous questions. plainType has one under the key "asks" that
// does not, next to another component. optType has an optional one, off
// by default.
const (
	talkType  = "test_talk"
	talkV1    = `{"components":[{"kind":"questions","params":{"anonymous":true}}]}`
	plainType = "test_plain_questions"
	plainV1   = `{"components":[{"kind":"questions","key":"asks","labels":{"tr":"Sorular"}},{"kind":"draw"}]}`
	optType   = "test_opt_questions"
	optV1     = `{"components":[{"kind":"questions","optional":{"default":false}}]}`
	key       = "questions"
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
	qs      *questions.Service
	events  *event.Service
	tayfas  *tayfa.Service
	reports *report.Service
	pool    *pgxpool.Pool
	admin   *pgxpool.Pool
	clock   *clock
}

// adults answers that nobody is under 18.
type adults struct{}

func (adults) IsMinor(_ context.Context, users []id.ID) (map[id.ID]bool, error) {
	out := make(map[id.ID]bool, len(users))
	for _, u := range users {
		out[u] = false
	}
	return out, nil
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
	reports, err := report.New(report.Config{Pool: d.App, Minors: adults{}, Now: c.now})
	must(t, err)
	e := &env{qs: questions.New(fw), events: events, tayfas: tayfas, reports: reports, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, talkType, talkV1)
	e.addType(t, plainType, plainV1)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Söyleşi Tayfası", ClientRequestID: id.New().String()})
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

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Söyleşi", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

func ref(user, tf, ev id.ID, k string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: k}
}

func on(b bool) *bool { return &b }

func ask(body string, anonymous bool) questions.Input {
	return questions.Input{Body: body, Anonymous: anonymous, ClientRequestID: id.New().String()}
}

// changes counts the event's component_changed events for k.
func (e *env) changes(t *testing.T, ev id.ID, k string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), k)
}

func byID(l questions.List) map[id.ID]questions.Question {
	out := map[id.ID]questions.Question{}
	for _, q := range l.Questions {
		out[q.ID] = q
	}
	return out
}

// Asking, anonymity, voting, answering and removing end to end.
func TestQuestionsEndToEnd(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, talkType)
	asker := e.going(t, tf, ev.ID, authz.Member)
	voter := e.going(t, tf, ev.ID, authz.Member)
	outsider := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)

	l, err := e.qs.List(ctx, ref(asker, tf, ev.ID, key))
	must(t, err)
	if l.Questions == nil || len(l.Questions) != 0 || !l.AnonymousAllowed || !l.CallerCanAsk || l.CallerCanMarkAnswered ||
		l.MaxPerPerson != questions.MaxPerPerson {
		t.Fatalf("boş bileşen: %+v", l)
	}
	if l, err := e.qs.List(ctx, ref(outsider, tf, ev.ID, key)); err != nil || l.CallerCanAsk {
		t.Fatalf("katılmayan üye soru sorabilir görünüyor: %+v %v", l, err)
	}
	if l, err := e.qs.List(ctx, ref(admin, tf, ev.ID, key)); err != nil || !l.CallerCanMarkAnswered {
		t.Fatalf("organizatör cevaplandı işaretleyemiyor görünüyor: %+v %v", l, err)
	}

	_, err = e.qs.Ask(ctx, ref(outsider, tf, ev.ID, key), ask("Neden bu rota?", false))
	wantErr(t, "katılmayan üye", err, apperr.ErrEventNotGoing)
	for name, tc := range map[string]struct {
		in    questions.Input
		field string
	}{
		"boş":             {ask("", false), "body"},
		"yalnızca boşluk": {ask(" \n\t ", false), "body"},
		"denetim":         {ask("Soru\u0007?", false), "body"},
		"sıfır genişlik":  {ask("So​ru?", false), "body"},
		"uzun":            {ask(strings.Repeat("a", questions.BodyMax+1), false), "body"},
		"istek kimliği":   {questions.Input{Body: "Soru?", ClientRequestID: "x"}, "client_request_id"},
	} {
		_, err := e.qs.Ask(ctx, ref(asker, tf, ev.ID, key), tc.in)
		wantField(t, name, err, tc.field)
	}

	before := e.changes(t, ev.ID, key)
	in := ask("  Neden   bu\n rota  seçildi? ", true)
	anon, err := e.qs.Ask(ctx, ref(asker, tf, ev.ID, key), in)
	must(t, err)
	if anon.Body != "Neden bu rota seçildi?" || !anon.Anonymous || anon.Author != (id.ID{}) || !anon.CallerIsAuthor || !anon.CallerCanRemove {
		t.Fatalf("anonim soru: %+v", anon)
	}
	again, err := e.qs.Ask(ctx, ref(asker, tf, ev.ID, key), in)
	must(t, err)
	if again.ID != anon.ID || again.Author != (id.ID{}) || e.count(t, `SELECT count(*) FROM event_questions`) != 1 {
		t.Fatalf("tekrar gönderim: %+v", again)
	}
	long, err := e.qs.Ask(ctx, ref(asker, tf, ev.ID, key), ask(strings.Repeat("ş", questions.BodyMax), false))
	must(t, err)
	named, err := e.qs.Ask(ctx, ref(voter, tf, ev.ID, key), ask("Mola olacak mı?", false))
	must(t, err)
	if named.Author != voter || named.Anonymous {
		t.Fatalf("açık soru: %+v", named)
	}
	if got := e.changes(t, ev.ID, key); got != before+3 {
		t.Fatalf("canlı güncelleme %d, %d bekleniyordu", got, before+3)
	}

	// Nobody but the server learns the anonymous question's author: not
	// another member, not a moderator, not the organizer; the author only
	// learns that it is theirs.
	for name, u := range map[string]id.ID{"üye": voter, "moderatör": moderator, "organizatör": admin, "yazar": asker} {
		l, err := e.qs.List(ctx, ref(u, tf, ev.ID, key))
		must(t, err)
		q, ok := byID(l)[anon.ID]
		if !ok || q.Author != (id.ID{}) || !q.Anonymous || q.CallerIsAuthor != (u == asker) {
			t.Errorf("%s anonim soruyu böyle görüyor: %+v", name, q)
		}
		if n := byID(l)[named.ID]; n.Author != voter {
			t.Errorf("%s açık sorunun yazarını görmüyor: %+v", name, n)
		}
	}
	// Nor the audit log the tayfa's admins read; no text anywhere.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE actor_id = $1 AND action LIKE 'event.question%'`, asker.PG()); n != 1 {
		t.Fatalf("anonim soru sorana ait denetim kaydı sayısı %d (yalnızca açık soru)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE actor_id = $1 AND action = $2`, voter.PG(), questions.AuditAsked); n != 1 {
		t.Fatalf("açık sorunun denetim kaydı %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%rota%' OR metadata::text LIKE '%Mola%'`); n != 0 {
		t.Fatal("soru metni denetim kaydına girdi")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%rota%' OR payload::text LIKE '%Mola%'`); n != 0 {
		t.Fatal("soru metni olaya girdi")
	}

	// Votes: one per person per question; most voted first.
	must(t, e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), anon.ID, true))
	must(t, e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), anon.ID, true))
	must(t, e.qs.SetVote(ctx, ref(asker, tf, ev.ID, key), anon.ID, true))
	wantErr(t, "katılmayan üyenin oyu", e.qs.SetVote(ctx, ref(outsider, tf, ev.ID, key), anon.ID, true), apperr.ErrEventNotGoing)
	wantErr(t, "bilinmeyen soruya oy", e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), id.New(), true), apperr.ErrEventItemNotFound)
	l, err = e.qs.List(ctx, ref(voter, tf, ev.ID, key))
	must(t, err)
	if len(l.Questions) != 3 || l.Questions[0].ID != anon.ID || l.Questions[0].VoteCount != 2 || !l.Questions[0].CallerVoted ||
		byID(l)[named.ID].CallerVoted || byID(l)[long.ID].VoteCount != 0 {
		t.Fatalf("oylar: %+v", l.Questions)
	}
	must(t, e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), anon.ID, false))
	must(t, e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), anon.ID, false))
	// Taking a vote back needs no going answer.
	must(t, e.qs.SetVote(ctx, ref(outsider, tf, ev.ID, key), anon.ID, false))
	if n := e.count(t, `SELECT count(*) FROM event_question_votes WHERE question_id = $1`, anon.ID.PG()); n != 1 {
		t.Fatalf("geri alınan oy: %d", n)
	}

	// Answered: the organizer, moderators and admins; answered questions
	// go last.
	wantErr(t, "üye cevaplandı işaretliyor", e.qs.SetAnswered(ctx, ref(asker, tf, ev.ID, key), anon.ID, true), apperr.ErrPermissionDenied)
	must(t, e.qs.SetAnswered(ctx, ref(admin, tf, ev.ID, key), anon.ID, true))
	must(t, e.qs.SetAnswered(ctx, ref(moderator, tf, ev.ID, key), anon.ID, true))
	wantErr(t, "bilinmeyen soru", e.qs.SetAnswered(ctx, ref(admin, tf, ev.ID, key), id.New(), true), apperr.ErrEventItemNotFound)
	l, err = e.qs.List(ctx, ref(voter, tf, ev.ID, key))
	must(t, err)
	if last := l.Questions[len(l.Questions)-1]; last.ID != anon.ID || last.AnsweredAt.IsZero() {
		t.Fatalf("cevaplanan soru sonda değil: %+v", l.Questions)
	}
	if n := e.count(t, `SELECT count(*) FROM event_questions WHERE id = $1 AND answered_by = $2`, anon.ID.PG(), admin.PG()); n != 1 {
		t.Fatal("ilk işaret değişti")
	}
	must(t, e.qs.SetAnswered(ctx, ref(moderator, tf, ev.ID, key), anon.ID, false))
	must(t, e.qs.SetAnswered(ctx, ref(moderator, tf, ev.ID, key), anon.ID, false))
	l, err = e.qs.List(ctx, ref(voter, tf, ev.ID, key))
	must(t, err)
	if !byID(l)[anon.ID].AnsweredAt.IsZero() {
		t.Fatal("işaret kalkmadı")
	}

	// Removing: another member may not; a moderator removes the anonymous
	// question without learning its author; the author removes their own.
	wantErr(t, "başkasının sorusunu üye kaldırıyor", e.qs.Remove(ctx, ref(voter, tf, ev.ID, key), anon.ID), apperr.ErrPermissionDenied)
	if q := byID(l)[anon.ID]; q.CallerCanRemove {
		t.Fatal("üye başkasının sorusunu kaldırabilir görünüyor")
	}
	must(t, e.qs.Remove(ctx, ref(moderator, tf, ev.ID, key), anon.ID))
	wantErr(t, "kaldırılmış soru", e.qs.Remove(ctx, ref(moderator, tf, ev.ID, key), anon.ID), apperr.ErrEventItemNotFound)
	wantErr(t, "kaldırılmış soruya oy", e.qs.SetVote(ctx, ref(voter, tf, ev.ID, key), anon.ID, true), apperr.ErrEventItemNotFound)
	own, err := e.qs.Ask(ctx, ref(asker, tf, ev.ID, key), ask("Bir anonim soru daha?", true))
	must(t, err)
	must(t, e.qs.Remove(ctx, ref(asker, tf, ev.ID, key), own.ID))
	must(t, e.qs.Remove(ctx, ref(voter, tf, ev.ID, key), named.ID))
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE actor_id = $1 AND action LIKE 'event.question%'`, asker.PG()); n != 1 {
		t.Fatalf("anonim sorusunu kaldıranın denetim kaydı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE actor_id = $1 AND action = $2`, moderator.PG(), questions.AuditRemoved); n != 1 {
		t.Fatalf("moderatörün kaldırma kaydı %d", n)
	}
	l, err = e.qs.List(ctx, ref(voter, tf, ev.ID, key))
	must(t, err)
	if len(l.Questions) != 1 || l.Questions[0].ID != long.ID {
		t.Fatalf("kalan sorular: %+v", l.Questions)
	}

	// Purged 30 days later with their votes.
	n, err := store.New(e.pool).PurgeEventQuestions(ctx, store.PurgeEventQuestionsParams{
		DeletedBefore: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, N: 100,
	})
	must(t, err)
	if n != 3 || e.count(t, `SELECT count(*) FROM event_question_votes`) != 0 || e.count(t, `SELECT count(*) FROM event_questions`) != 1 {
		t.Fatalf("kalıcı silme: %d", n)
	}
}

// A component without the anonymous parameter takes no anonymous question;
// its key comes from the definition.
func TestQuestionsAnonymousOff(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, plainType)
	u := e.going(t, tf, ev.ID, authz.Member)
	l, err := e.qs.List(ctx, ref(u, tf, ev.ID, "asks"))
	must(t, err)
	if l.AnonymousAllowed || !l.CallerCanAsk {
		t.Fatalf("bileşen: %+v", l)
	}
	_, err = e.qs.Ask(ctx, ref(u, tf, ev.ID, "asks"), ask("Gizli soru?", true))
	wantField(t, "anonim kapalı", err, "anonymous")
	q, err := e.qs.Ask(ctx, ref(u, tf, ev.ID, "asks"), ask("Açık soru?", false))
	must(t, err)
	if q.Author != u {
		t.Fatalf("soru: %+v", q)
	}
}

// The write window, the limits and the roles (ADR-0025 §5).
func TestQuestionsWriteWindowAndLimits(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, talkType)
	u := e.going(t, tf, ev.ID, authz.Member)
	muted := e.going(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, muted)
	moderator := e.member(t, tf, authz.Moderator)

	_, err := e.qs.Ask(ctx, ref(muted, tf, ev.ID, key), ask("Susturulmuş soru?", false))
	wantErr(t, "susturulmuş üye", err, apperr.ErrMuted)
	if l, err := e.qs.List(ctx, ref(muted, tf, ev.ID, key)); err != nil || l.CallerCanAsk {
		t.Fatalf("susturulmuş üye okuyamıyor ya da sorabilir görünüyor: %+v %v", l, err)
	}

	var first questions.Question
	for i := range questions.MaxPerPerson {
		q, err := e.qs.Ask(ctx, ref(u, tf, ev.ID, key), ask("Soru "+strings.Repeat("?", i+1), false))
		must(t, err)
		if i == 0 {
			first = q
		}
	}
	_, err = e.qs.Ask(ctx, ref(u, tf, ev.ID, key), ask("Bir tane daha?", false))
	wantErr(t, "kişi başı sınır", err, apperr.ErrEventLimit)
	// The bound is on live questions.
	must(t, e.qs.Remove(ctx, ref(u, tf, ev.ID, key), first.ID))
	_, err = e.qs.Ask(ctx, ref(u, tf, ev.ID, key), ask("Yerine bir soru?", false))
	must(t, err)

	// Seven days after the end the component freezes: no asking, voting,
	// answering or removing one's own; a moderator still removes.
	l, err := e.qs.List(ctx, ref(u, tf, ev.ID, key))
	must(t, err)
	q := l.Questions[0]
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.qs.Ask(ctx, ref(u, tf, ev.ID, key), ask("Geç soru?", false))
	wantErr(t, "donmuş bileşene soru", err, apperr.ErrComponentFrozen)
	wantErr(t, "donmuş bileşende oy", e.qs.SetVote(ctx, ref(u, tf, ev.ID, key), q.ID, true), apperr.ErrComponentFrozen)
	wantErr(t, "donmuş bileşende işaret", e.qs.SetAnswered(ctx, ref(admin, tf, ev.ID, key), q.ID, true), apperr.ErrComponentFrozen)
	wantErr(t, "donmuş bileşende kendi sorusunu kaldırma", e.qs.Remove(ctx, ref(u, tf, ev.ID, key), q.ID), apperr.ErrComponentFrozen)
	l, err = e.qs.List(ctx, ref(u, tf, ev.ID, key))
	must(t, err)
	if l.CallerCanAsk || byID(l)[q.ID].CallerCanRemove {
		t.Fatalf("donmuş bileşende yazma izni: %+v", l)
	}
	if l, err := e.qs.List(ctx, ref(admin, tf, ev.ID, key)); err != nil || l.CallerCanMarkAnswered || !byID(l)[q.ID].CallerCanRemove {
		t.Fatalf("donmuş bileşende yönetici: %+v %v", l, err)
	}
	must(t, e.qs.Remove(ctx, ref(moderator, tf, ev.ID, key), q.ID))

	// A cancelled event takes no question; a moderator still removes.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, talkType)
	u2 := e.going(t, tf, cancelled.ID, authz.Member)
	kept, err := e.qs.Ask(ctx, ref(u2, tf, cancelled.ID, key), ask("İptal olur mu?", true))
	must(t, err)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.qs.Ask(ctx, ref(u2, tf, cancelled.ID, key), ask("Neden iptal?", false))
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	must(t, e.qs.Remove(ctx, ref(moderator, tf, cancelled.ID, key), kept.ID))
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on. The
// trigger and the CHECKs refuse writes that go around the service. IDOR.
func TestQuestionsOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	talk := e.newEvent(t, tf, admin, talkType)
	off := e.newEvent(t, tf, admin, optType)
	opened := e.newEvent(t, tf, admin, optType, component.Selection{Key: key, Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":       ref(admin, tf, general.ID, key),
		"başka türün anahtarı": ref(admin, tf, talk.ID, "asks"),
	} {
		_, err := e.qs.List(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.qs.Ask(ctx, r, ask("Soru?", false))
		wantErr(t, name+" sorma", err, apperr.ErrComponentNotFound)
	}
	_, err := e.qs.List(ctx, ref(admin, tf, off.ID, key))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.qs.Ask(ctx, ref(admin, tf, off.ID, key), ask("Soru?", false))
	wantErr(t, "kapalı bileşene sorma", err, apperr.ErrComponentDisabled)
	u := e.going(t, tf, opened.ID, authz.Member)
	_, err = e.qs.Ask(ctx, ref(u, tf, opened.ID, key), ask("Açılmış bileşene soru?", false))
	must(t, err)

	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, client_request_id)
			VALUES (gen_random_uuid(), $1, 'questions', gen_random_uuid(), false, 'Soru?', gen_random_uuid())`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
	for name, body := range map[string]string{"sekme": "So\tru?", "boş": "", "kırpılmamış": " Soru?", "uzun": strings.Repeat("a", 501)} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, client_request_id)
			VALUES (gen_random_uuid(), $1, 'questions', gen_random_uuid(), false, $2, gen_random_uuid())`, talk.ID.PG(), body)
		if c := constraintOf(err); c != "event_questions_body" {
			t.Errorf("%s metin: kısıt %q (%v)", name, c, err)
		}
	}

	// Another tayfa's event does not exist through this tayfa; a question
	// of another event is not found through this one.
	q, err := e.qs.Ask(ctx, ref(u, tf, opened.ID, key), ask("Başka etkinlikten görünür mü?", false))
	must(t, err)
	tf2, admin2 := e.newTayfa(t)
	_, err = e.qs.List(ctx, ref(admin2, tf2, opened.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	wantErr(t, "başka tayfa kaldırma", e.qs.Remove(ctx, ref(admin2, tf2, opened.ID, key), q.ID), apperr.ErrEventNotFound)
	wantErr(t, "başka etkinliğin sorusu", e.qs.Remove(ctx, ref(admin, tf, talk.ID, key), q.ID), apperr.ErrEventItemNotFound)
	wantErr(t, "başka etkinliğin sorusuna işaret", e.qs.SetAnswered(ctx, ref(admin, tf, talk.ID, key), q.ID, true), apperr.ErrEventItemNotFound)
	_, err = e.qs.List(ctx, ref(id.New(), tf, opened.ID, key))
	wantErr(t, "üye değil", err, apperr.ErrTayfaNotFound)
}

// A report about an anonymous question keeps its author for the platform
// only: the tayfa's queue has no subject, and member_sanctioned cannot
// close it (ADR-0025 §5).
func TestQuestionsReportHidesAnonymousAuthor(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, talkType)
	author := e.going(t, tf, ev.ID, authz.Member)
	reporter := e.going(t, tf, ev.ID, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)

	anon, err := e.qs.Ask(ctx, ref(author, tf, ev.ID, key), ask("Anonim ve kaba bir soru?", true))
	must(t, err)
	named, err := e.qs.Ask(ctx, ref(author, tf, ev.ID, key), ask("Açık ve kaba bir soru?", false))
	must(t, err)

	_, _, err = e.reports.Create(ctx, author, tf, report.CreateInput{TargetType: report.TargetEventQuestion, TargetID: anon.ID.String(), Reason: report.ReasonSpam})
	wantField(t, "kendi sorusunu şikâyet", err, "target_id")
	_, _, err = e.reports.Create(ctx, reporter, tf, report.CreateInput{TargetType: report.TargetEventQuestion, TargetID: id.New().String(), Reason: report.ReasonSpam})
	wantErr(t, "bilinmeyen soru", err, apperr.ErrReportTargetNotFound)
	_, created, err := e.reports.Create(ctx, reporter, tf, report.CreateInput{TargetType: report.TargetEventQuestion, TargetID: anon.ID.String(), Reason: report.ReasonHarassment})
	must(t, err)
	if !created {
		t.Fatal("şikâyet açılmadı")
	}
	_, _, err = e.reports.Create(ctx, reporter, tf, report.CreateInput{TargetType: report.TargetEventQuestion, TargetID: named.ID.String(), Reason: report.ReasonHarassment})
	must(t, err)

	// The platform keeps the author on the report.
	if n := e.count(t, `SELECT count(*) FROM reports WHERE target_id = $1 AND subject_user_id = $2 AND subject_hidden`, anon.ID.PG(), author.PG()); n != 1 {
		t.Fatalf("şikâyet kaydında yazar tutulmadı: %d", n)
	}
	queue, _, err := e.reports.ListQueue(ctx, moderator, tf, report.StatusOpen, 0, "")
	must(t, err)
	var hidden, shown report.ModView
	for _, v := range queue {
		switch v.TargetID {
		case anon.ID:
			hidden = v
		case named.ID:
			shown = v
		}
	}
	if hidden.ID == (id.ID{}) || hidden.TargetType != report.TargetEventQuestion || hidden.SubjectID != (id.ID{}) {
		t.Fatalf("anonim sorunun şikâyeti kuyrukta: %+v", hidden)
	}
	if shown.SubjectID != author {
		t.Fatalf("açık sorunun şikâyetinde yazar: %+v", shown)
	}

	// The tayfa cannot sanction someone it does not know; it removes the
	// question and closes the report.
	_, err = e.reports.Resolve(ctx, moderator, tf, hidden.ID, report.ResolutionMemberSanctioned)
	wantErr(t, "yazarı gizli şikâyete yaptırım", err, apperr.ErrReportActionMissing)
	_, err = e.reports.Resolve(ctx, moderator, tf, hidden.ID, report.ResolutionContentRemoved)
	wantErr(t, "kaldırılmamış soru", err, apperr.ErrReportActionMissing)
	must(t, e.qs.Remove(ctx, ref(moderator, tf, ev.ID, key), anon.ID))
	v, err := e.reports.Resolve(ctx, moderator, tf, hidden.ID, report.ResolutionContentRemoved)
	must(t, err)
	if v.SubjectID != (id.ID{}) || v.Status != report.StatusResolved {
		t.Fatalf("çözülen şikâyet: %+v", v)
	}
	resolved, _, err := e.reports.ListQueue(ctx, moderator, tf, report.StatusResolved, 0, "")
	must(t, err)
	for _, r := range resolved {
		if r.TargetID == anon.ID && r.SubjectID != (id.ID{}) {
			t.Fatalf("çözülmüşlerde yazar: %+v", r)
		}
	}

	// The mark cannot be taken off.
	if _, err := e.pool.Exec(ctx, `UPDATE reports SET subject_hidden = false WHERE target_id = $1`, anon.ID.PG()); err == nil {
		t.Fatal("yazar gizliliği kaldırıldı")
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, priority, platform_only, created_at, subject_hidden)
		VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'event', gen_random_uuid(), 'spam', 'normal', false, now(), true)`, tf.PG()); constraintOf(err) != "reports_subject_hidden" {
		t.Fatalf("başka hedefte yazar gizliliği: %v", err)
	}
}
