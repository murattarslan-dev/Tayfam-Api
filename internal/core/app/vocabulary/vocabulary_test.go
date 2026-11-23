package vocabulary_test

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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/logbook"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func TestNormalizeAndClean(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  KIRLANGIÇ   Balığı ", "kırlangıç balığı"},
		{"İstavrit", "istavrit"},
		{"Levrek\tbalığı", "levrek balığı"},
	} {
		if got := vocabulary.Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, %q bekleniyordu", tc.in, got, tc.want)
		}
	}
	if got, ok := vocabulary.Clean("  Ev   kırlangıcı ", 60); !ok || got != "Ev kırlangıcı" {
		t.Errorf("Clean: %q %v", got, ok)
	}
	for name, tc := range map[string]struct {
		in  string
		max int
	}{
		"boş":            {"   ", 60},
		"denetim kodu":   {"Lev\x00rek", 60},
		"61 karakter":    {strings.Repeat("ş", 61), 60},
		"alanın sınırı":  {"Levrek", 5},
		"geçersiz UTF-8": {"Lev\xffrek", 60},
	} {
		if _, ok := vocabulary.Clean(tc.in, tc.max); ok {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	// A field bound above 60 characters is still bound by the vocabulary.
	if _, ok := vocabulary.Clean(strings.Repeat("a", 61), 120); ok {
		t.Error("sözlük sınırı aşıldı")
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type env struct {
	voc    *vocabulary.Service
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
	return &env{voc: vocabulary.New(fw), logs: logbook.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
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

// fishing creates a fishing event starting at start and an attended angler.
func (e *env) fishing(t *testing.T, tf, organizer id.ID, start time.Time) (ev, angler id.ID) {
	t.Helper()
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: event.TypeFishing, Title: "Akşam oltası", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(),
	})
	must(t, err)
	angler = e.member(t, tf, authz.Member)
	_, err = e.events.SetRSVP(ctx, angler, tf, v.ID, event.RSVPGoing)
	must(t, err)
	e.exec(t, `UPDATE event_rsvps SET attendance = 'attended', attendance_method = 'location', checked_in_at = now()
		WHERE event_id = $1 AND user_id = $2`, v.ID.PG(), angler.PG())
	return v.ID, angler
}

// catch logs species and returns the vocabulary value it chose.
func (e *env) catch(t *testing.T, user, tf, ev id.ID, species string) (entry, value id.ID) {
	t.Helper()
	text, err := json.Marshal(species)
	must(t, err)
	got, err := e.logs.Add(ctx, component.Ref{User: user, Tayfa: tf, Event: ev, Key: "catches"}, logbook.Input{
		Values: map[string]json.RawMessage{"species": text}, ClientRequestID: id.New().String(),
	})
	must(t, err)
	return got.ID, got.VocabularyIDs["species"]
}

func ids(l vocabulary.List) []id.ID {
	out := make([]id.ID, 0, len(l.Values))
	for _, v := range l.Values {
		out = append(out, v.ID)
	}
	return out
}

const ns = "fish_species"

// Searching, merging and undoing, with the permission table, the scope
// (IDOR), the audit record and the statistics hook.
func TestVocabulary(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	start := time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC).Add(-3 * time.Hour) // 2026-12-31 21:30 UTC = 2027-01-01 00:30 Istanbul
	e.clock.t = start.Add(-48 * time.Hour)
	ev, angler := e.fishing(t, tf, admin, start)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	entryLavrak, lavrak := e.catch(t, angler, tf, ev, "lavrak")
	_, levrek := e.catch(t, angler, tf, ev, "Levrek")
	_, levrekBaligi := e.catch(t, angler, tf, ev, "Levrek balığı")
	_, cipura := e.catch(t, angler, tf, ev, "Çipura")

	// Every member reads; those that start with the query first.
	all, err := e.voc.List(ctx, member, tf, ns, "", false)
	must(t, err)
	if len(all.Values) != 4 || all.CallerCanMerge {
		t.Fatalf("liste: %+v", all)
	}
	for _, q := range []string{"lev", "LEV", "  Lev "} {
		l, err := e.voc.List(ctx, member, tf, ns, q, false)
		must(t, err)
		if got := ids(l); len(got) != 2 || got[0] != levrek || got[1] != levrekBaligi {
			t.Fatalf("arama %q: %+v", q, l)
		}
	}
	l, err := e.voc.List(ctx, member, tf, ns, "balığı", false)
	must(t, err)
	if got := ids(l); len(got) != 1 || got[0] != levrekBaligi {
		t.Fatalf("içeren arama: %+v", l)
	}
	l, err = e.voc.List(ctx, moderator, tf, ns, "", false)
	must(t, err)
	if !l.CallerCanMerge {
		t.Fatal("moderatör birleştiremiyor görünüyor")
	}
	l, err = e.voc.List(ctx, member, tf, "book", "", false)
	must(t, err)
	if len(l.Values) != 0 {
		t.Fatalf("başka ad alanı: %+v", l)
	}

	// Only moderators and admins merge; not muted ones.
	_, err = e.voc.Merge(ctx, member, tf, ns, lavrak, levrek)
	wantErr(t, "üye", err, apperr.ErrPermissionDenied)
	muted := e.member(t, tf, authz.Moderator)
	// Muted until 30 days after the test clock.
	e.exec(t, `UPDATE memberships SET muted_until = $4, muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`,
		tf.PG(), muted.PG(), id.New().PG(), e.clock.now().Add(30*24*time.Hour))
	_, err = e.voc.Merge(ctx, muted, tf, ns, lavrak, levrek)
	wantErr(t, "susturulmuş moderatör", err, apperr.ErrMuted)
	_, err = e.voc.List(ctx, muted, tf, ns, "", false)
	must(t, err)

	v, err := e.voc.Merge(ctx, moderator, tf, ns, lavrak, levrek)
	must(t, err)
	if v.ID != lavrak || v.Value != "lavrak" || v.MergedInto != levrek || v.MergedIntoValue != "Levrek" {
		t.Fatalf("birleştirme: %+v", v)
	}
	// The merged value resolves to its target, also when searched.
	l, err = e.voc.List(ctx, member, tf, ns, "lav", false)
	must(t, err)
	if got := ids(l); len(got) != 1 || got[0] != levrek {
		t.Fatalf("birleştirilmiş arama: %+v", l)
	}
	l, err = e.voc.List(ctx, member, tf, ns, "", true)
	must(t, err)
	if len(l.Values) != 1 || l.Values[0].ID != lavrak || l.Values[0].MergedInto != levrek || l.Values[0].MergedIntoValue != "Levrek" {
		t.Fatalf("birleştirilmişler: %+v", l)
	}
	page, err := e.logs.List(ctx, component.Ref{User: member, Tayfa: tf, Event: ev, Key: "catches"}, 0, "")
	must(t, err)
	for _, x := range page.Entries {
		if x.ID == entryLavrak && (x.VocabularyIDs["species"] != levrek || string(x.Values["species"]) != `"Levrek"`) {
			t.Fatalf("birleştirilmiş kayıt: %+v", x)
		}
	}
	// The audit record and the event name values by ID only; the event
	// gives the statistics the year (Europe/Istanbul) to recompute.
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'target_id' = $3
		AND metadata ->> 'namespace' = $4 AND metadata::text !~* 'lavrak|levrek'`, vocabulary.AuditMerged, lavrak.PG(), levrek.String(), ns) != 1 {
		t.Fatal("denetim kaydı")
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = $1 AND subject_id = $2`, vocabulary.TypeMerged, lavrak.PG()).Scan(&payload))
	var p struct {
		Tayfa     string  `json:"tayfa_id"`
		Namespace string  `json:"namespace"`
		Value     string  `json:"vocabulary_id"`
		Target    string  `json:"target_id"`
		Years     []int32 `json:"years"`
	}
	must(t, json.Unmarshal([]byte(payload), &p))
	if p.Tayfa != tf.String() || p.Namespace != ns || p.Value != lavrak.String() || p.Target != levrek.String() ||
		len(p.Years) != 1 || p.Years[0] != 2027 || strings.Contains(strings.ToLower(payload), "lavrak") {
		t.Fatalf("olay: %s", payload)
	}

	// Merges stay one level deep.
	_, err = e.voc.Merge(ctx, moderator, tf, ns, lavrak, cipura)
	wantErr(t, "birleştirilmiş kaynak", err, apperr.ErrVocabularyMerged)
	_, err = e.voc.Merge(ctx, moderator, tf, ns, cipura, lavrak)
	wantErr(t, "birleştirilmiş hedef", err, apperr.ErrVocabularyMerged)
	// Merging a target moves its merged values along.
	_, err = e.voc.Merge(ctx, admin, tf, ns, levrek, levrekBaligi)
	must(t, err)
	if e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE id = $1 AND merged_into = $2`, lavrak.PG(), levrekBaligi.PG()) != 1 {
		t.Fatal("bağlı değer hedefe taşınmadı")
	}

	// Undo: the value is itself again; the values that moved stay.
	must(t, e.voc.Unmerge(ctx, moderator, tf, ns, levrek))
	l, err = e.voc.List(ctx, member, tf, ns, "levrek", false)
	must(t, err)
	if got := ids(l); len(got) != 2 {
		t.Fatalf("geri alınan değer: %+v", l)
	}
	err = e.voc.Unmerge(ctx, moderator, tf, ns, levrek)
	wantErr(t, "birleştirilmemiş", err, apperr.ErrVocabularyNotMerged)
	if e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2`, vocabulary.TypeUnmerged, levrek.PG()) != 1 ||
		e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, vocabulary.AuditUnmerged, levrek.PG()) != 1 {
		t.Fatal("geri almanın kaydı")
	}
	err = e.voc.Unmerge(ctx, member, tf, ns, lavrak)
	wantErr(t, "üye geri alamaz", err, apperr.ErrPermissionDenied)

	// Bad input, unknown values, another tayfa's and another namespace's
	// values (IDOR).
	_, err = e.voc.Merge(ctx, moderator, tf, ns, cipura, cipura)
	wantField(t, "kendisine", err, "target_id")
	_, err = e.voc.Merge(ctx, moderator, tf, ns, cipura, id.New())
	wantErr(t, "bilinmeyen hedef", err, apperr.ErrVocabularyNotFound)
	tf2, admin2 := e.newTayfa(t)
	ev2, angler2 := e.fishing(t, tf2, admin2, start)
	_, theirs := e.catch(t, angler2, tf2, ev2, "Levrek")
	_, err = e.voc.Merge(ctx, moderator, tf, ns, cipura, theirs)
	wantErr(t, "başka tayfanın değeri", err, apperr.ErrVocabularyNotFound)
	_, err = e.voc.Merge(ctx, admin2, tf2, ns, theirs, cipura)
	wantErr(t, "başka tayfanın değerini birleştirme", err, apperr.ErrVocabularyNotFound)
	_, err = e.voc.Merge(ctx, moderator, tf, "book", cipura, levrek)
	wantErr(t, "başka ad alanı", err, apperr.ErrVocabularyNotFound)
	_, err = e.voc.List(ctx, id.New(), tf, ns, "", false)
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	_, err = e.voc.List(ctx, member, tf, "Fish", "", false)
	wantField(t, "bozuk ad alanı", err, "namespace")
	_, err = e.voc.List(ctx, member, tf, ns, strings.Repeat("a", 61), false)
	wantField(t, "uzun arama", err, "query")

	// A read-only tayfa is read, not merged.
	e.exec(t, `UPDATE tayfas SET status = 'archived', archived_at = now() WHERE id = $1`, tf.PG())
	l, err = e.voc.List(ctx, moderator, tf, ns, "", false)
	must(t, err)
	if l.CallerCanMerge {
		t.Fatal("salt okunur tayfada birleştirme açık görünüyor")
	}
	_, err = e.voc.Merge(ctx, moderator, tf, ns, cipura, levrek)
	wantErr(t, "salt okunur", err, apperr.ErrTayfaReadOnly)
}

// Account deletion keeps the values and clears who added them; export
// lists a person's entries with their vocabulary values (ADR-0037 §4).
func TestVocabularyPersonalData(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev, angler := e.fishing(t, tf, admin, e.clock.now().Add(24*time.Hour))
	entry, levrek := e.catch(t, angler, tf, ev, "Levrek")

	q := store.New(e.pool)
	rows, err := q.ExportLogEntries(ctx, store.ExportLogEntriesParams{UserID: angler.PG(), After: pgtype.UUID{Valid: true}, N: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("dışa aktarma: %d", len(rows))
	}
	var data struct {
		ID         string `json:"id"`
		Vocabulary map[string]struct {
			ID    string `json:"vocabulary_id"`
			Value string `json:"value"`
		} `json:"vocabulary"`
	}
	must(t, json.Unmarshal(rows[0].Data, &data))
	if data.ID != entry.String() || data.Vocabulary["species"].ID != levrek.String() || data.Vocabulary["species"].Value != "Levrek" {
		t.Fatalf("dışa aktarılan kayıt: %s", rows[0].Data)
	}

	must(t, q.ClearVocabularyCreator(ctx, angler.PG()))
	if e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE id = $1 AND created_by IS NULL AND value = 'Levrek'`, levrek.PG()) != 1 {
		t.Fatal("hesap silmede değer ya da ekleyen")
	}
}
