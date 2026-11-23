package vote_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vote"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types (Notion Faz 3): freeType a free vote (kitap buluşması:
// sıradaki kitap); peopleType a participants vote that a celebration reads
// (hackathon); carType a vehicles vote with a celebration (araç buluşması);
// photoType an album vote (fotoğraf yürüyüşü); optType an optional free vote
// that is off by default, and a second version without it.
const (
	freeType   = "test_vote_free"
	freeV1     = `{"components":[{"kind":"vote","params":{"source":"free"}}]}`
	peopleType = "test_vote_people"
	peopleV1   = `{"components":[{"kind":"vote","key":"mvp","params":{"source":"participants"}},{"kind":"celebration","params":{"source":"mvp"}}]}`
	carType    = "test_vote_car"
	carV1      = `{"components":[{"kind":"vehicles"},{"kind":"vote","params":{"source":"vehicles"}},{"kind":"celebration","params":{"source":"vote"}}]}`
	photoType  = "test_vote_photo"
	photoV1    = `{"components":[{"kind":"vote","params":{"source":"album"}}]}`
	optType    = "test_vote_opt"
	optV1      = `{"components":[{"kind":"vote","optional":{"default":false},"params":{"source":"free"}}]}`
	optV2      = `{"components":[{"kind":"draw"}]}`
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
	fw           *component.Service
	votes        *vote.Service
	celebrations *celebration.Service
	events       *event.Service
	tayfas       *tayfa.Service
	pool         *pgxpool.Pool
	admin        *pgxpool.Pool
	clock        *clock
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
		fw: fw, votes: vote.New(fw), celebrations: celebration.New(fw), events: events, tayfas: tayfas,
		pool: d.App, admin: d.Admin, clock: c,
	}
	for key, def := range map[string]string{freeType: freeV1, peopleType: peopleV1, carType: carV1, photoType: photoV1, optType: optV1} {
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Oylama Tayfası", ClientRequestID: id.New().String()})
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

// going adds a member who answered "going".
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
		Type: typ, Title: "Pazar buluşması", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
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

// option returns the option with the given label or ID.
func option(t *testing.T, v vote.Vote, match func(vote.Option) bool) vote.Option {
	t.Helper()
	i := slices.IndexFunc(v.Options, match)
	if i < 0 {
		t.Fatalf("seçenek yok: %+v", v.Options)
	}
	return v.Options[i]
}

func labelled(l string) func(vote.Option) bool {
	return func(o vote.Option) bool { return o.Label == l }
}

func withID(x id.ID) func(vote.Option) bool { return func(o vote.Option) bool { return o.ID == x } }

// counts returns the options' counts in order.
func counts(v vote.Vote) []int32 {
	out := make([]int32, 0, len(v.Options))
	for _, o := range v.Options {
		out = append(out, o.VoteCount)
	}
	return out
}

// A free vote: the organizer writes the options, participants vote in
// secret, the counts show once the organizer closes it.
func TestVoteFree(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, freeType)
	const key = "vote"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.attended(t, tf, ev.ID, authz.Member)
	c := e.going(t, tf, ev.ID, authz.Member)
	bystander := e.member(t, tf, authz.Member)

	got, err := e.votes.Get(ctx, r(bystander))
	must(t, err)
	if got.Source != vote.SourceFree || len(got.Options) != 0 || got.Closed || got.ResultsVisible || got.CallerCanVote ||
		got.CallerCanEditOptions || got.CallerCanClose || got.MaxOptions != vote.MaxOptions || got.VoterCount != 0 {
		t.Fatalf("başlangıç: %+v", got)
	}
	if got, err := e.votes.Get(ctx, r(organizer)); err != nil || !got.CallerCanEditOptions || !got.CallerCanClose || got.CallerCanVote {
		t.Fatalf("organizatör: %+v %v", got, err)
	}
	if got, err := e.votes.Get(ctx, r(a)); err != nil || !got.CallerCanVote || got.CallerCanEditOptions || got.CallerCanClose {
		t.Fatalf("katılımcı: %+v %v", got, err)
	}

	// Options: cleaned, unique in Turkish lower case, at most MaxOptions;
	// the organizer's only.
	for name, label := range map[string]string{
		"boş": "  ", "uzun": strings.Repeat("ş", vote.LabelMax+1), "denetim karakteri": "Kitap\u0000", "görünmez": "Kit\u200bap",
	} {
		_, err := e.votes.AddOption(ctx, r(organizer), label)
		wantField(t, name, err, "label")
	}
	_, err = e.votes.AddOption(ctx, r(a), "Suç ve Ceza")
	wantErr(t, "katılımcı seçenek ekler", err, apperr.ErrPermissionDenied)
	base := e.changes(t, ev.ID, key)
	got, err = e.votes.AddOption(ctx, r(organizer), "  İnce   Memed\n")
	must(t, err)
	if len(got.Options) != 1 || got.Options[0].Label != "İnce Memed" || got.Options[0].User != (id.ID{}) {
		t.Fatalf("seçenek: %+v", got.Options)
	}
	_, err = e.votes.AddOption(ctx, r(organizer), "ince memed")
	wantErr(t, "aynı seçenek", err, apperr.ErrVoteOptionExists)
	for _, l := range []string{"Suç ve Ceza", "Tutunamayanlar"} {
		got, err = e.votes.AddOption(ctx, r(organizer), l)
		must(t, err)
	}
	if n := e.changes(t, ev.ID, key); n != base+3 {
		t.Fatalf("canlı güncelleme: %d", n-base)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, vote.AuditOptionAdded, ev.ID.PG()); n != 3 {
		t.Errorf("denetim kaydı: %d", n)
	}
	memed, suc, tut := option(t, got, labelled("İnce Memed")).ID, option(t, got, labelled("Suç ve Ceza")).ID, option(t, got, labelled("Tutunamayanlar")).ID

	// Votes: one per person, replaced by a new one; secret; counts hidden.
	_, err = e.votes.Cast(ctx, r(bystander), memed)
	wantErr(t, "katılımcı olmayan", err, apperr.ErrEventNotGoing)
	_, err = e.votes.Cast(ctx, r(a), id.New())
	wantErr(t, "bilinmeyen seçenek", err, apperr.ErrEventItemNotFound)
	got, err = e.votes.Cast(ctx, r(a), memed)
	must(t, err)
	if got.CallerOption != memed || got.VoterCount != 1 || got.ResultsVisible || !slices.Equal(counts(got), []int32{0, 0, 0}) {
		t.Fatalf("oy: %+v", got)
	}
	base = e.changes(t, ev.ID, key)
	_, err = e.votes.Cast(ctx, r(a), memed)
	must(t, err)
	if e.changes(t, ev.ID, key) != base {
		t.Fatal("aynı oy yeniden yazıldı")
	}
	_, err = e.votes.Cast(ctx, r(a), suc)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(b), suc)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(c), tut)
	must(t, err)
	got, err = e.votes.Get(ctx, r(bystander))
	must(t, err)
	if got.CallerOption != (id.ID{}) || got.VoterCount != 3 || !slices.Equal(counts(got), []int32{0, 0, 0}) || slices.ContainsFunc(got.Options, func(o vote.Option) bool { return o.Winner }) {
		t.Fatalf("gizli sonuç: %+v", got)
	}
	if got, err := e.votes.Get(ctx, r(organizer)); err != nil || !slices.Equal(counts(got), []int32{0, 0, 0}) {
		t.Fatalf("organizatöre de gizli: %+v %v", got, err)
	}
	// Withdrawing: c's vote goes; again is no error.
	got, err = e.votes.Withdraw(ctx, r(c))
	must(t, err)
	if got.CallerOption != (id.ID{}) || got.VoterCount != 2 {
		t.Fatalf("geri alma: %+v", got)
	}
	_, err = e.votes.Withdraw(ctx, r(c))
	must(t, err)
	_, err = e.votes.Cast(ctx, r(c), tut)
	must(t, err)

	// Removing an option takes its votes.
	extra, err := e.votes.AddOption(ctx, r(organizer), "Kürk Mantolu Madonna")
	must(t, err)
	madonna := option(t, extra, labelled("Kürk Mantolu Madonna")).ID
	_, err = e.votes.Cast(ctx, r(c), madonna)
	must(t, err)
	got, err = e.votes.RemoveOption(ctx, r(organizer), madonna)
	must(t, err)
	if len(got.Options) != 3 || got.VoterCount != 2 || e.count(t, `SELECT count(*) FROM event_votes WHERE event_id = $1`, ev.ID.PG()) != 2 {
		t.Fatalf("seçenek kaldırma: %+v", got)
	}
	_, err = e.votes.RemoveOption(ctx, r(organizer), madonna)
	wantErr(t, "kaldırılmış seçenek", err, apperr.ErrEventItemNotFound)
	_, err = e.votes.Cast(ctx, r(c), tut)
	must(t, err)

	// The limit.
	for i := len(got.Options); i < vote.MaxOptions; i++ {
		_, err = e.votes.AddOption(ctx, r(organizer), "Kitap "+strings.Repeat("a", i))
		must(t, err)
	}
	_, err = e.votes.AddOption(ctx, r(organizer), "Bir fazlası")
	wantErr(t, "sınır", err, apperr.ErrEventLimit)

	// Closing: counts and winners show, nothing changes any more.
	_, err = e.votes.Close(ctx, r(a))
	wantErr(t, "katılımcı kapatır", err, apperr.ErrPermissionDenied)
	e.clock.set(ev.StartsAt.Add(time.Hour))
	got, err = e.votes.Close(ctx, r(organizer))
	must(t, err)
	if !got.Closed || !got.ClosedAt.Equal(e.clock.now()) || !got.ResultsVisible || got.CallerCanClose || got.CallerCanEditOptions {
		t.Fatalf("kapanış: %+v", got)
	}
	got, err = e.votes.Get(ctx, r(bystander))
	must(t, err)
	if o := option(t, got, withID(suc)); o.VoteCount != 2 || !o.Winner {
		t.Fatalf("kazanan: %+v", o)
	}
	if o := option(t, got, withID(tut)); o.VoteCount != 1 || o.Winner {
		t.Fatalf("ikinci: %+v", o)
	}
	if o := option(t, got, withID(memed)); o.VoteCount != 0 || o.Winner {
		t.Fatalf("oysuz: %+v", o)
	}
	if got.CallerCanVote {
		t.Fatal("kapalı oylamada oy izni")
	}
	_, err = e.votes.Cast(ctx, r(a), tut)
	wantErr(t, "kapalıda oy", err, apperr.ErrVoteClosed)
	_, err = e.votes.Withdraw(ctx, r(a))
	wantErr(t, "kapalıda geri alma", err, apperr.ErrVoteClosed)
	_, err = e.votes.AddOption(ctx, r(organizer), "Geç kalan")
	wantErr(t, "kapalıda seçenek", err, apperr.ErrVoteClosed)
	_, err = e.votes.RemoveOption(ctx, r(organizer), tut)
	wantErr(t, "kapalıda kaldırma", err, apperr.ErrVoteClosed)
	again, err := e.votes.Close(ctx, r(organizer))
	must(t, err)
	if !again.ClosedAt.Equal(got.ClosedAt) {
		t.Fatalf("yeniden kapatma: %+v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, vote.AuditClosed, ev.ID.PG()); n != 1 {
		t.Errorf("kapanış denetim kaydı: %d", n)
	}

	// The ballot is secret: no audit record, event or log names a vote.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.vote%'
		AND (action NOT IN ($2, $3, $4) OR metadata::text LIKE '%' || $5 || '%' OR actor_id = $6)`,
		ev.ID.PG(), vote.AuditOptionAdded, vote.AuditOptionRemoved, vote.AuditClosed, a.String(), a.PG()); n != 0 {
		t.Errorf("denetim kaydında oy: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE subject_id = $1 AND payload::text LIKE '%' || $2 || '%'`, ev.ID.PG(), suc.String()); n != 0 {
		t.Errorf("olayda oy: %d", n)
	}
}

// A participants vote: the options are the participants, one may not vote
// for oneself, a vote for someone who no longer comes does not count, and a
// closed vote's winner is the celebration's.
func TestVoteParticipantsAndCelebration(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, peopleType)
	const key = "mvp"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.going(t, tf, ev.ID, authz.Member)
	c := e.attended(t, tf, ev.ID, authz.Member)
	d := e.going(t, tf, ev.ID, authz.Member)
	maybe := e.member(t, tf, authz.Member)
	_, err := e.events.SetRSVP(ctx, maybe, tf, ev.ID, event.RSVPMaybe)
	must(t, err)

	got, err := e.votes.Get(ctx, r(a))
	must(t, err)
	people := make([]id.ID, 0, len(got.Options))
	for _, o := range got.Options {
		if o.ID != o.User || o.Label != "" || o.Vehicle != nil {
			t.Fatalf("katılımcı seçeneği: %+v", o)
		}
		people = append(people, o.User)
	}
	want := []id.ID{a, b, c, d}
	slices.SortFunc(want, func(x, y id.ID) int { return slices.Compare(x[:], y[:]) })
	if !slices.Equal(people, want) {
		t.Fatalf("seçenekler: %v, %v bekleniyordu", people, want)
	}
	_, err = e.votes.AddOption(ctx, r(organizer), "Serbest")
	wantErr(t, "katılımcı oylamasında seçenek", err, apperr.ErrVoteOptionsFixed)
	_, err = e.votes.RemoveOption(ctx, r(organizer), a)
	wantErr(t, "katılımcı oylamasında kaldırma", err, apperr.ErrVoteOptionsFixed)
	if got.CallerCanEditOptions {
		t.Fatal("katılımcı oylamasında seçenek düzenleme izni")
	}
	_, err = e.votes.Cast(ctx, r(a), a)
	wantErr(t, "kendine oy", err, apperr.ErrVoteOwnOption)
	_, err = e.votes.Cast(ctx, r(a), maybe)
	wantErr(t, "katılımcı olmayana oy", err, apperr.ErrEventItemNotFound)
	_, err = e.votes.Cast(ctx, r(a), d)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(b), d)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(c), a)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(d), a)
	must(t, err)
	// d no longer comes: the votes for d do not count (d's own vote does).
	_, err = e.events.SetRSVP(ctx, d, tf, ev.ID, event.RSVPNotGoing)
	must(t, err)
	// The trigger refuses a vote for oneself that goes around the service.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_votes (id, event_id, component_key, user_id, candidate_user_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4, now(), now())`, id.New().PG(), ev.ID.PG(), key, maybe.PG())
	if cst := constraintOf(err); cst != "event_votes_not_self" {
		t.Errorf("kendine oy kısıtı: %q (%v)", cst, err)
	}
	// ... and a target of another source's kind.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_votes (id, event_id, component_key, user_id, photo_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())`, id.New().PG(), ev.ID.PG(), key, maybe.PG(), id.New().PG())
	if cst := constraintOf(err); cst != "event_votes_target_guard" {
		t.Errorf("hedef tetikleyicisi: %q (%v)", cst, err)
	}

	// Before the close the celebration has no winner.
	e.clock.set(ev.StartsAt.Add(time.Hour))
	cel, err := e.celebrations.Get(ctx, ref(organizer, tf, ev.ID, "celebration"))
	must(t, err)
	if cel.SourceKind != "vote" || cel.Source != key || len(cel.Winners) != 0 {
		t.Fatalf("kapanmadan kutlama: %+v", cel)
	}
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, "celebration"), nil)
	wantErr(t, "kapanmadan ilan", err, apperr.ErrCelebrationNoWinner)

	got, err = e.votes.Close(ctx, r(organizer))
	must(t, err)
	if got.VoterCount != 2 || slices.ContainsFunc(got.Options, func(o vote.Option) bool { return o.ID == d }) {
		t.Fatalf("ayrılan katılımcı: %+v", got)
	}
	if o := option(t, got, withID(a)); o.VoteCount != 2 || !o.Winner {
		t.Fatalf("kazanan: %+v", o)
	}
	// a voted for d: a's own vote no longer names an option.
	if mine, err := e.votes.Get(ctx, r(a)); err != nil || mine.CallerOption != (id.ID{}) {
		t.Fatalf("geçersiz kalan oy: %+v %v", mine, err)
	}

	cel, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, "celebration"), nil)
	must(t, err)
	if len(cel.Winners) != 1 || cel.Winners[0].User != a || cel.Winners[0].VoteOption != a {
		t.Fatalf("kutlama: %+v", cel)
	}
	// Load gives the same to the readers that build on it.
	tv, err := catalog.TypeVersion(ctx, store.New(e.pool), peopleType, ev.TypeVersion)
	must(t, err)
	comp, _ := tv.Parsed.Component(key)
	res, err := vote.Load(ctx, store.New(e.pool), ev.ID, tv.Parsed, comp)
	must(t, err)
	if ws := res.Winners(); len(ws) != 1 || ws[0].User != a || !res.Closed {
		t.Fatalf("Load: %+v", res)
	}
}

// A vehicles vote: the options are the vehicles of the vehicles component
// (no plate), not one's own; a vehicle that goes takes its votes; the
// celebration's winner is the owner with the vehicle.
func TestVoteVehicles(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, carType)
	const key = "vote"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	owner1 := e.going(t, tf, ev.ID, authz.Member)
	owner2 := e.going(t, tf, ev.ID, authz.Member)
	voter := e.going(t, tf, ev.ID, authz.Member)
	car1, car2 := id.New(), id.New()
	e.exec(t, `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make, model, model_year, color, plate, created_at) VALUES
		($1, $3, 'vehicles', $4, 'Ford', 'Mustang', 1967, 'Kırmızı', '34 ABC 123', now() - interval '1 minute'),
		($2, $3, 'vehicles', $5, 'Fiat', NULL, NULL, NULL, NULL, now())`, car1.PG(), car2.PG(), ev.ID.PG(), owner1.PG(), owner2.PG())

	got, err := e.votes.Get(ctx, r(voter))
	must(t, err)
	if len(got.Options) != 2 || got.Options[0].ID != car1 || got.Options[0].User != owner1 || got.Options[1].ID != car2 {
		t.Fatalf("araç seçenekleri: %+v", got.Options)
	}
	if v := got.Options[0].Vehicle; v == nil || v.Make != "Ford" || v.Model != "Mustang" || v.ModelYear != 1967 || v.Color != "Kırmızı" {
		t.Fatalf("araç: %+v", got.Options[0].Vehicle)
	}
	if v := got.Options[1].Vehicle; v == nil || v.Make != "Fiat" || v.Model != "" || v.ModelYear != 0 {
		t.Fatalf("eksik araç: %+v", got.Options[1].Vehicle)
	}
	_, err = e.votes.Cast(ctx, r(owner1), car1)
	wantErr(t, "kendi aracına oy", err, apperr.ErrVoteOwnOption)
	_, err = e.votes.Cast(ctx, r(owner1), car2)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(voter), car2)
	must(t, err)
	_, err = e.votes.Cast(ctx, r(owner2), car1)
	must(t, err)

	// The vehicle goes with its votes (owner2's car).
	_, err = e.admin.Exec(ctx, `DELETE FROM event_vehicles WHERE id = $1`, car2.PG())
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_votes WHERE event_id = $1`, ev.ID.PG()); n != 1 {
		t.Fatalf("silinen aracın oyları: %d", n)
	}

	e.clock.set(ev.StartsAt.Add(time.Hour))
	_, err = e.votes.Close(ctx, r(organizer))
	must(t, err)
	cel, err := e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, "celebration"), nil)
	must(t, err)
	if len(cel.Winners) != 1 || cel.Winners[0].User != owner1 || cel.Winners[0].VoteOption != car1 {
		t.Fatalf("kutlama: %+v", cel)
	}
}

// An album vote: the options are the album's photos; not one's own; a
// removed photo takes its votes.
func TestVoteAlbum(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, photoType)
	const key = "vote"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	shooter := e.attended(t, tf, ev.ID, authz.Member)
	voter := e.attended(t, tf, ev.ID, authz.Member)
	photo, gone, upload := id.New(), id.New(), id.New()
	e.exec(t, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, client_request_id, created_at, deleted_at, deleted_by) VALUES
		($1, $3, $4, $5, gen_random_uuid(), now() - interval '1 minute', NULL, NULL),
		($2, $3, $4, gen_random_uuid(), gen_random_uuid(), now(), NULL, NULL),
		(gen_random_uuid(), $3, $4, gen_random_uuid(), gen_random_uuid(), now(), now(), $4)`,
		photo.PG(), gone.PG(), ev.ID.PG(), shooter.PG(), upload.PG())

	got, err := e.votes.Get(ctx, r(voter))
	must(t, err)
	if len(got.Options) != 2 || got.Options[0].ID != photo || got.Options[0].User != shooter || got.Options[0].Upload != upload {
		t.Fatalf("fotoğraf seçenekleri: %+v", got.Options)
	}
	_, err = e.votes.Cast(ctx, r(shooter), photo)
	wantErr(t, "kendi fotoğrafına oy", err, apperr.ErrVoteOwnOption)
	_, err = e.votes.Cast(ctx, r(voter), gone)
	must(t, err)
	// The photo is removed (soft delete): its votes go.
	_, err = e.admin.Exec(ctx, `UPDATE event_photos SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, gone.PG(), organizer.PG())
	must(t, err)
	got, err = e.votes.Get(ctx, r(voter))
	must(t, err)
	if len(got.Options) != 1 || got.VoterCount != 0 || got.CallerOption != (id.ID{}) ||
		e.count(t, `SELECT count(*) FROM event_votes WHERE event_id = $1`, ev.ID.PG()) != 0 {
		t.Fatalf("silinen fotoğraf: %+v", got)
	}
	_, err = e.votes.Cast(ctx, r(voter), gone)
	wantErr(t, "silinen fotoğrafa oy", err, apperr.ErrEventItemNotFound)
	got, err = e.votes.Cast(ctx, r(voter), photo)
	must(t, err)
	if got.CallerOption != photo {
		t.Fatalf("oy: %+v", got)
	}
}

// Frozen: the counts show without a close, nothing changes; a cancelled
// event takes nothing.
func TestVoteFrozenAndCancelled(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, freeType)
	cancelled := e.newEvent(t, tf, organizer, freeType)
	const key = "vote"
	a := e.going(t, tf, ev.ID, authz.Member)
	got, err := e.votes.AddOption(ctx, ref(organizer, tf, ev.ID, key), "Bir")
	must(t, err)
	one := got.Options[0].ID
	_, err = e.votes.Cast(ctx, ref(a, tf, ev.ID, key), one)
	must(t, err)
	_, err = e.votes.AddOption(ctx, ref(organizer, tf, cancelled.ID, key), "Bir")
	must(t, err)
	_, err = e.events.Cancel(ctx, organizer, tf, cancelled.ID, "")
	must(t, err)

	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	got, err = e.votes.Get(ctx, ref(a, tf, ev.ID, key))
	must(t, err)
	if got.Closed || !got.ResultsVisible || got.CallerCanVote || got.Options[0].VoteCount != 1 || !got.Options[0].Winner {
		t.Fatalf("donmuş: %+v", got)
	}
	_, err = e.votes.Withdraw(ctx, ref(a, tf, ev.ID, key))
	wantErr(t, "donmuşta geri alma", err, apperr.ErrComponentFrozen)
	_, err = e.votes.Close(ctx, ref(organizer, tf, ev.ID, key))
	wantErr(t, "donmuşta kapatma", err, apperr.ErrComponentFrozen)

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	c := e.member(t, tf, authz.Member)
	_, err = e.votes.Close(ctx, ref(organizer, tf, cancelled.ID, key))
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.votes.Get(ctx, ref(c, tf, cancelled.ID, key)); err != nil || got.CallerCanVote || len(got.Options) != 1 {
		t.Fatalf("iptal edilmiş okuma: %+v %v", got, err)
	}
}

// The component outside the event's type version, switched off, or in
// another tayfa.
func TestVoteOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	free := e.newEvent(t, tf, admin, freeType)
	off := e.newEvent(t, tf, admin, optType)
	onEv := e.newEvent(t, tf, admin, optType, component.Selection{Key: "vote", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "vote"),
		"tanımda olmayan anahtar": ref(admin, tf, free.ID, "mvp"),
	} {
		_, err := e.votes.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.votes.AddOption(ctx, r, "Bir")
		wantErr(t, name+" seçenek", err, apperr.ErrComponentNotFound)
	}
	_, err := e.votes.Get(ctx, ref(admin, tf, off.ID, "vote"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.votes.AddOption(ctx, ref(admin, tf, off.ID, "vote"), "Bir")
	wantErr(t, "kapalı bileşende seçenek", err, apperr.ErrComponentDisabled)
	if _, err := e.votes.AddOption(ctx, ref(admin, tf, onEv.ID, "vote"), "Bir"); err != nil {
		t.Fatalf("açılan bileşen: %v", err)
	}

	// The type's next version drops the vote.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, optType, optV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, optType, optV2)
	newer := e.newEvent(t, tf, admin, optType)
	if newer.TypeVersion != 2 {
		t.Fatalf("tür sürümü: %d", newer.TypeVersion)
	}
	_, err = e.votes.AddOption(ctx, ref(admin, tf, newer.ID, "vote"), "Bir")
	wantErr(t, "yeni sürümde olmayan oylama", err, apperr.ErrComponentNotFound)

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_vote_options (id, event_id, component_key, label, label_normalized, position, created_by)
			VALUES ($1, $2, 'vote', 'Bir', 'bir', 1, $3)`, id.New().PG(), ev.PG(), admin.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
		_, err = e.pool.Exec(ctx, `INSERT INTO event_vote_closures (event_id, component_key, closed_by, closed_at) VALUES ($1, 'vote', $2, now())`,
			ev.PG(), admin.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s kapanış: tetikleyici %q (%v)", name, c, err)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.votes.Get(ctx, ref(admin2, tf2, free.ID, "vote"))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.votes.Close(ctx, ref(admin2, tf2, free.ID, "vote"))
	wantErr(t, "başka tayfa kapatma", err, apperr.ErrEventNotFound)
}

// Who may do what, for every role.
func TestVotePermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, freeType)
	const key = "vote"
	organizer := e.member(t, tf, authz.Member)
	own := e.newEvent(t, tf, admin, freeType)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())
	moderator := e.going(t, tf, ev.ID, authz.Moderator)
	member := e.going(t, tf, ev.ID, authz.Member)
	_, err := e.events.SetRSVP(ctx, member, tf, own.ID, event.RSVPGoing)
	must(t, err)
	notGoing := e.member(t, tf, authz.Member)
	muted := e.going(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, muted)
	referee := e.going(t, tf, ev.ID, authz.Member)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key})
	must(t, err)
	for _, x := range []id.ID{ev.ID, own.ID} {
		_, err := e.votes.AddOption(ctx, ref(admin, tf, x, key), "Bir")
		must(t, err)
	}
	optionOf := func(x id.ID) id.ID {
		got, err := e.votes.Get(ctx, ref(admin, tf, x, key))
		must(t, err)
		return got.Options[0].ID
	}

	// Options and closing: managers.
	for _, tc := range []struct {
		name string
		user id.ID
		ev   id.ID
		want error
	}{
		{"yönetici", admin, ev.ID, nil},
		{"moderatör", moderator, ev.ID, nil},
		{"kendi etkinliğinin organizatörü", organizer, own.ID, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev.ID, apperr.ErrPermissionDenied},
		{"atanmış hakem", referee, ev.ID, apperr.ErrPermissionDenied},
		{"üye", member, ev.ID, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev.ID, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev.ID, apperr.ErrTayfaNotFound},
	} {
		_, err := e.votes.AddOption(ctx, ref(tc.user, tf, tc.ev, key), "Seçenek "+tc.name)
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s seçenek: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		got, err := e.votes.Get(ctx, ref(tc.user, tf, tc.ev, key))
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		must(t, err)
		if got.CallerCanEditOptions != (tc.want == nil) || got.CallerCanClose != (tc.want == nil) {
			t.Errorf("%s: düzenleme %v, kapatma %v", tc.name, got.CallerCanEditOptions, got.CallerCanClose)
		}
	}

	// Voting: participants who are not muted.
	for _, tc := range []struct {
		name string
		user id.ID
		ev   id.ID
		want error
	}{
		{"gelen moderatör", moderator, ev.ID, nil},
		{"gelen üye", member, ev.ID, nil},
		{"gelen hakem", referee, ev.ID, nil},
		{"kendi etkinliğinde gelen üye", member, own.ID, nil},
		{"gelmeyen yönetici", admin, ev.ID, apperr.ErrEventNotGoing},
		{"gelmeyen üye", notGoing, ev.ID, apperr.ErrEventNotGoing},
		{"susturulmuş üye", muted, ev.ID, apperr.ErrMuted},
		{"üye değil", id.New(), ev.ID, apperr.ErrTayfaNotFound},
	} {
		_, err := e.votes.Cast(ctx, ref(tc.user, tf, tc.ev, key), optionOf(tc.ev))
		if tc.want == nil {
			must(t, err)
		} else {
			wantErr(t, tc.name+" oy", err, tc.want)
		}
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			continue
		}
		got, err := e.votes.Get(ctx, ref(tc.user, tf, tc.ev, key))
		must(t, err)
		if got.CallerCanVote != (tc.want == nil) {
			t.Errorf("%s: caller_can_vote = %v", tc.name, got.CallerCanVote)
		}
	}
	_, err = e.votes.Withdraw(ctx, ref(muted, tf, ev.ID, key))
	wantErr(t, "susturulmuş geri alma", err, apperr.ErrMuted)
}
