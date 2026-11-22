package poollink_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/poollink"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types: sessionType links up to 50 questions (the default), and
// tinyType up to 2.
const (
	sessionType = "test_session"
	sessionV1   = `{"components":[{"kind":"pool_link","key":"pool"}]}`
	tinyType    = "test_tiny_session"
	tinyV1      = `{"components":[{"kind":"pool_link","params":{"max_links":2}}]}`
)

const studyTags = `INSERT INTO catalog_study_tags (key, parent_key, position, labels, status) VALUES
	('level_a', NULL, 1, '{"tr": "Düzey A"}', 'active'),
	('level_a_other', 'level_a', 99, '{"tr": "Diğer"}', 'active'),
	('subject_a', 'level_a', 1, '{"tr": "Ders A"}', 'active'),
	('subject_a_other', 'subject_a', 99, '{"tr": "Diğer"}', 'active')`

type env struct {
	links  *poollink.Service
	qa     *qa.Service
	events *event.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	now    time.Time
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

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{pool: d.App, admin: d.Admin, now: time.Now().UTC().Truncate(time.Microsecond)}
	now := func() time.Time { return e.now }
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: now})
	must(t, err)
	e.links = poollink.New(fw)
	e.qa, err = qa.New(qa.Config{Pool: d.App, Now: now})
	must(t, err)
	e.events, err = event.New(event.Config{Pool: d.App, Log: log, Now: now})
	must(t, err)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	e.addType(t, sessionType, sessionV1)
	e.addType(t, tinyType, tinyV1)
	_, err = d.Admin.Exec(ctx, studyTags)
	must(t, err)
	return e
}

// addType writes a type as the platform would (the catalog is read-only
// for the service).
func (e *env) addType(t *testing.T, key, def string) {
	t.Helper()
	_, err := e.admin.Exec(ctx, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
	must(t, err)
	_, err = e.admin.Exec(ctx, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
	must(t, err)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Etüt Tayfası", ClientRequestID: id.New().String()})
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

func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string) event.Event {
	t.Helper()
	start := e.now.Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Soru çözüm oturumu", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(),
	})
	must(t, err)
	return v
}

func (e *env) ask(t *testing.T, tf, author id.ID, body string) id.ID {
	t.Helper()
	v, err := e.qa.CreateQuestion(ctx, author, tf, qa.CreateQuestionInput{
		Pool: qa.PoolQuestionPool, SubjectKey: "subject_a", Body: body, ClientRequestID: id.New().String(),
	})
	must(t, err)
	return v.ID
}

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func ids(l poollink.Links) []id.ID {
	var out []id.ID
	for _, q := range l.Questions {
		out = append(out, q.ID)
	}
	return out
}

// The list end to end: set in order, read, replace, the same list again,
// a deleted question, remove.
func TestPoolLinks(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, sessionType)
	r := ref(admin, tf, ev.ID, "pool")
	q1, q2, q3 := e.ask(t, tf, member, "Birinci soru"), e.ask(t, tf, member, "İkinci soru"), e.ask(t, tf, member, "Üçüncü soru")

	got, err := e.links.Get(ctx, ref(member, tf, ev.ID, "pool"))
	must(t, err)
	if len(got.Questions) != 0 || got.MaxLinks != poollink.MaxLinks || got.CallerCanEdit {
		t.Fatalf("boş liste: %+v", got)
	}
	set, err := e.links.Set(ctx, r, []id.ID{q2, q1})
	must(t, err)
	if !slices.Equal(ids(set), []id.ID{q2, q1}) || !set.CallerCanEdit || set.Questions[0].Body != "İkinci soru" ||
		set.Questions[0].SubjectKey != "subject_a" || set.Questions[0].AuthorID != member {
		t.Fatalf("yazılan liste: %+v", set)
	}
	got, err = e.links.Get(ctx, ref(member, tf, ev.ID, "pool"))
	if err != nil || !slices.Equal(ids(got), []id.ID{q2, q1}) || got.CallerCanEdit {
		t.Fatalf("üyenin okuduğu liste: %+v %v", got, err)
	}
	changes := func() int {
		return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2`, component.EventChanged, ev.ID.PG())
	}
	if changes() != 1 {
		t.Fatalf("canlı güncelleme = %d", changes())
	}
	// The same list again changes nothing; a new order does.
	_, err = e.links.Set(ctx, r, []id.ID{q2, q1})
	must(t, err)
	if changes() != 1 {
		t.Fatalf("aynı liste olay yazdı: %d", changes())
	}
	set, err = e.links.Set(ctx, r, []id.ID{q1, q3, q2})
	if err != nil || !slices.Equal(ids(set), []id.ID{q1, q3, q2}) || changes() != 2 {
		t.Fatalf("yeni sıra: %+v %v %d", set, err, changes())
	}
	// A deleted question disappears from the list; restored, it is back.
	must(t, e.qa.DeleteQuestion(ctx, admin, tf, q3))
	got, err = e.links.Get(ctx, r)
	if err != nil || !slices.Equal(ids(got), []id.ID{q1, q2}) {
		t.Fatalf("silinen soru listede: %+v %v", got, err)
	}
	_, err = e.qa.RestoreQuestion(ctx, admin, tf, q3)
	must(t, err)
	got, err = e.links.Get(ctx, r)
	if err != nil || !slices.Equal(ids(got), []id.ID{q1, q3, q2}) {
		t.Fatalf("geri alınan soru: %+v %v", got, err)
	}
	// No text in the outbox or the audit record.
	for _, sql := range []string{
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Birinci%'`,
		`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Birinci%'`,
	} {
		if n := e.count(t, sql); n != 0 {
			t.Fatalf("metin sızdı: %s", sql)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, poollink.AuditSet, ev.ID.PG()); n != 2 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	// Empty removes every link.
	set, err = e.links.Set(ctx, r, nil)
	if err != nil || len(set.Questions) != 0 || e.count(t, `SELECT count(*) FROM event_pool_links WHERE event_id = $1`, ev.ID.PG()) != 0 {
		t.Fatalf("kaldırma: %+v %v", set, err)
	}
}

// Only managers write; the questions must be visible pool questions of the
// tayfa; the limits; the module.
func TestPoolLinkRules(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	ev := e.newEvent(t, tf, admin, sessionType)
	tiny := e.newEvent(t, tf, admin, tinyType)
	q1, q2, q3 := e.ask(t, tf, member, "Bir"), e.ask(t, tf, member, "İki"), e.ask(t, tf, member, "Üç")

	_, err := e.links.Set(ctx, ref(member, tf, ev.ID, "pool"), []id.ID{q1})
	wantErr(t, "üye yazdı", err, apperr.ErrPermissionDenied)
	if _, err := e.links.Set(ctx, ref(moderator, tf, ev.ID, "pool"), []id.ID{q1}); err != nil {
		t.Fatalf("moderatör yazamadı: %v", err)
	}

	_, err = e.links.Set(ctx, ref(admin, tf, ev.ID, "pool"), []id.ID{q1, q1})
	wantField(t, "tekrar eden soru", err, poollink.FieldQuestionIDs)
	_, err = e.links.Set(ctx, ref(admin, tf, tiny.ID, "pool_link"), []id.ID{q1, q2, q3})
	wantField(t, "max_links aşıldı", err, poollink.FieldQuestionIDs)
	if got, err := e.links.Get(ctx, ref(admin, tf, tiny.ID, "pool_link")); err != nil || got.MaxLinks != 2 {
		t.Fatalf("max_links: %+v %v", got, err)
	}
	many := make([]id.ID, poollink.MaxLinks+1)
	for i := range many {
		many[i] = id.New()
	}
	_, err = e.links.Set(ctx, ref(admin, tf, ev.ID, "pool"), many)
	wantField(t, "51 soru", err, poollink.FieldQuestionIDs)

	// Unknown, a Soru-Cevap question, another tayfa's, a deleted one.
	plain, err := e.qa.CreateQuestion(ctx, member, tf, qa.CreateQuestionInput{Title: "Kamp yeri neresi olsun?", ClientRequestID: id.New().String()})
	must(t, err)
	otherTf, otherAdmin := e.newTayfa(t)
	foreign := e.ask(t, otherTf, otherAdmin, "Başka tayfa")
	gone := e.ask(t, tf, member, "Silinecek")
	must(t, e.qa.DeleteQuestion(ctx, member, tf, gone))
	for name, q := range map[string]id.ID{"bilinmeyen": id.New(), "Soru-Cevap": plain.ID, "başka tayfa": foreign, "silinmiş": gone} {
		_, err := e.links.Set(ctx, ref(admin, tf, ev.ID, "pool"), []id.ID{q1, q})
		wantErr(t, name, err, apperr.ErrQuestionNotFound)
	}
	// The database refuses another tayfa's question too.
	if _, err := e.pool.Exec(ctx, `INSERT INTO event_pool_links (event_id, component_key, question_id, position) VALUES ($1, 'pool', $2, 5)`,
		ev.ID.PG(), foreign.PG()); err == nil {
		t.Fatal("başka tayfanın sorusu bağlandı")
	}

	// The question pool off: the component is not shown.
	must(t, e.tayfas.SetModule(ctx, admin, tf, "question_pool", false))
	_, err = e.links.Get(ctx, ref(member, tf, ev.ID, "pool"))
	wantErr(t, "modül kapalı okuma", err, apperr.ErrModuleDisabled)
	_, err = e.links.Set(ctx, ref(admin, tf, ev.ID, "pool"), nil)
	wantErr(t, "modül kapalı yazma", err, apperr.ErrModuleDisabled)
	// Another tayfa's member reaches nothing.
	_, err = e.links.Get(ctx, ref(otherAdmin, tf, ev.ID, "pool"))
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
}
