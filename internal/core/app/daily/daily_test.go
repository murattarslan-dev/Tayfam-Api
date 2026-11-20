package daily_test

import (
	"context"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/daily"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func TestCleanBody(t *testing.T) {
	for in, want := range map[string]string{
		"  Ne mutlu   Türküm diyene! ":             "Ne mutlu Türküm diyene!",
		"Satır bir  \r\n\r\n\r\n  satır\tiki \n\n": "Satır bir\n\nsatır iki",
		"\n\nbir\nüç\n":                            "bir\nüç",
	} {
		if got, ok := daily.CleanBody(in); !ok || got != want {
			t.Errorf("CleanBody(%q) = %q %v, %q bekleniyordu", in, got, ok, want)
		}
	}
	for name, in := range map[string]string{
		"boş":             " \n\t ",
		"denetim kodu":    "bir\x00iki",
		"görünmez işaret": "bir\u200biki",
		"geçersiz UTF-8":  "bir\xffiki",
		"501 karakter":    strings.Repeat("ş", 501),
	} {
		if _, ok := daily.CleanBody(in); ok {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	if got, ok := daily.CleanBody(strings.Repeat("ş", 500)); !ok || len([]rune(got)) != 500 {
		t.Error("500 karakter reddedildi")
	}
}

func TestCleanAttribution(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "",
		"   ":                       "",
		" Mustafa  Kemal\nAtatürk ": "Mustafa Kemal Atatürk",
	} {
		if got, ok := daily.CleanAttribution(in); !ok || got != want {
			t.Errorf("CleanAttribution(%q) = %q %v, %q bekleniyordu", in, got, ok, want)
		}
	}
	for _, in := range []string{"Yunus\x07Emre", "Yunus\u202eEmre", strings.Repeat("a", 121)} {
		if _, ok := daily.CleanAttribution(in); ok {
			t.Errorf("CleanAttribution(%q) kabul edildi", in)
		}
	}
}

func TestValidPin(t *testing.T) {
	for _, p := range []*daily.Pin{nil, {Month: 11, Day: 10}, {Month: 2, Day: 29}, {Month: 2, Day: 29, Year: 2028}, {Month: 12, Day: 31, Year: 2100}} {
		if !daily.ValidPin(p) {
			t.Errorf("geçerli sabitleme reddedildi: %+v", p)
		}
	}
	for _, p := range []*daily.Pin{{Month: 0, Day: 1}, {Month: 13, Day: 1}, {Month: 4, Day: 31}, {Month: 2, Day: 30},
		{Month: 2, Day: 29, Year: 2027}, {Month: 1, Day: 1, Year: 1999}, {Month: 1, Day: 1, Year: 2101}, {Month: 1, Day: 0}, {Month: 1, Day: 1, Year: -1}} {
		if daily.ValidPin(p) {
			t.Errorf("geçersiz sabitleme kabul edildi: %+v", p)
		}
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

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC()
}

type env struct {
	svc    *daily.Service
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

// istanbul returns the instant of a wall clock time in Europe/Istanbul
// (UTC+3 all year).
func istanbul(y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, time.UTC).Add(-3 * time.Hour)
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &clock{t: istanbul(2026, time.November, 20, 10, 0)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	return &env{svc: daily.New(fw), tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
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

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Okuma Tayfası", ClientRequestID: id.New().String()})
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

func (e *env) add(t *testing.T, user, tf id.ID, body string, pin *daily.Pin) daily.Saved {
	t.Helper()
	s, err := e.svc.Create(ctx, user, tf, daily.Input{Body: body, Pin: pin}, id.New().String())
	must(t, err)
	return s
}

// today returns the body of today's item for user, "" when none.
func (e *env) today(t *testing.T, user, tf id.ID) (string, bool) {
	t.Helper()
	got, err := e.svc.Today(ctx, user, tf)
	must(t, err)
	if got.Item == nil {
		return "", got.Pinned
	}
	return got.Item.Body, got.Pinned
}

func (e *env) wantToday(t *testing.T, what string, user, tf id.ID, body string, pinned bool) {
	t.Helper()
	got, p := e.today(t, user, tf)
	if got != body || p != pinned {
		t.Fatalf("%s: %q (sabit %v), %q (sabit %v) bekleniyordu", what, got, p, body, pinned)
	}
}

// The rotation: one step per day the card is asked for, the same item all
// day for everyone, Europe/Istanbul days, back to the start after the
// last, no step on a day nobody asks.
func TestRotation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)

	e.wantToday(t, "boş havuz", member, tf, "", false)
	e.add(t, admin, tf, "A", nil)
	e.add(t, admin, tf, "B", nil)
	e.add(t, admin, tf, "C", nil)

	got, err := e.svc.Today(ctx, member, tf)
	must(t, err)
	if got.Item == nil || got.Item.Body != "A" || got.Date != time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("ilk gün: %+v", got)
	}
	// The same day, the same item, for every member; a late add changes
	// nothing today.
	e.add(t, admin, tf, "D", nil)
	e.clock.set(istanbul(2026, time.November, 20, 23, 59))
	e.wantToday(t, "aynı gün", admin, tf, "A", false)
	e.wantToday(t, "aynı gün, başka üye", member, tf, "A", false)

	// Midnight in Istanbul (21:00 UTC) is the next day.
	e.clock.set(istanbul(2026, time.November, 21, 0, 0))
	e.wantToday(t, "ertesi gün", member, tf, "B", false)

	// Nobody asks for three days: the rotation waits.
	e.clock.set(istanbul(2026, time.November, 25, 9, 0))
	e.wantToday(t, "boşluktan sonra", member, tf, "C", false)
	e.clock.set(istanbul(2026, time.November, 26, 9, 0))
	e.wantToday(t, "geç eklenen", member, tf, "D", false)
	e.clock.set(istanbul(2026, time.November, 27, 9, 0))
	e.wantToday(t, "başa dönüş", member, tf, "A", false)
}

// Pins: a pinned item wins its day (its year or every year), the first in
// pool order among several; a second pin on a day is reported, not
// refused; pinned items are not in the rotation and a pinned day does not
// move it.
func TestPins(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	mod := e.member(t, tf, authz.Moderator)
	member := e.member(t, tf, authz.Member)

	e.add(t, admin, tf, "A", nil)
	every := e.add(t, mod, tf, "Atatürk sözü", &daily.Pin{Month: 11, Day: 10})
	if every.PinConflict || every.Item.Pin == nil || every.Item.Pin.Year != 0 {
		t.Fatalf("her yıl sabitleme: %+v", every)
	}
	once := e.add(t, mod, tf, "Bu yıla özel", &daily.Pin{Month: 11, Day: 10, Year: 2026})
	if !once.PinConflict {
		t.Fatal("aynı güne ikinci sabitleme çakışma bildirmedi")
	}
	other := e.add(t, mod, tf, "Gelecek yıl", &daily.Pin{Month: 11, Day: 10, Year: 2027})
	if !other.PinConflict {
		t.Fatal("her yılla çakışan yıl bildirilmedi")
	}
	teacher := e.add(t, mod, tf, "Eğitim dizesi", &daily.Pin{Month: 11, Day: 24})
	if teacher.PinConflict {
		t.Fatal("başka gün çakışma sayıldı")
	}
	e.add(t, admin, tf, "B", nil)

	// Sabitlenen gün: sırada önce gelen sabitleme; sıra ilerlemez.
	e.clock.set(istanbul(2026, time.November, 10, 8, 0))
	e.wantToday(t, "10 Kasım 2026", member, tf, "Atatürk sözü", true)
	e.clock.set(istanbul(2026, time.November, 11, 8, 0))
	e.wantToday(t, "sabitlemeden sonra", member, tf, "A", false)
	e.clock.set(istanbul(2026, time.November, 12, 8, 0))
	e.wantToday(t, "sıradaki", member, tf, "B", false)
	// Pinned items are skipped by the rotation.
	e.clock.set(istanbul(2026, time.November, 13, 8, 0))
	e.wantToday(t, "dönüş", member, tf, "A", false)
	e.clock.set(istanbul(2026, time.November, 24, 8, 0))
	e.wantToday(t, "24 Kasım", member, tf, "Eğitim dizesi", true)

	// The order decides among pins of a day.
	l, err := e.svc.List(ctx, admin, tf, false)
	must(t, err)
	order := []id.ID{once.Item.ID}
	for _, it := range l.Items {
		if it.ID != once.Item.ID {
			order = append(order, it.ID)
		}
	}
	_, err = e.svc.Reorder(ctx, mod, tf, order)
	must(t, err)
	e.clock.set(istanbul(2026, time.November, 10, 23, 0))
	e.wantToday(t, "sıra değişince", member, tf, "Bu yıla özel", true)
	// Another year: the item pinned to 2026 is not shown; the item pinned
	// to every year is.
	e.clock.set(istanbul(2028, time.November, 10, 8, 0))
	e.wantToday(t, "10 Kasım 2028", member, tf, "Atatürk sözü", true)
	e.clock.set(istanbul(2027, time.November, 10, 8, 0))
	e.wantToday(t, "10 Kasım 2027", member, tf, "Atatürk sözü", true)

	// Unpinning puts an item back in the rotation; pinning takes it out
	// even when it was chosen today.
	e.clock.set(istanbul(2026, time.December, 1, 8, 0))
	shown, _ := e.today(t, member, tf)
	var shownID id.ID
	l, err = e.svc.List(ctx, member, tf, false)
	must(t, err)
	for _, it := range l.Items {
		if it.Body == shown {
			shownID = it.ID
		}
	}
	_, err = e.svc.Update(ctx, mod, tf, shownID, daily.Input{Body: shown, Pin: &daily.Pin{Month: 6, Day: 1}})
	must(t, err)
	if got, _ := e.today(t, member, tf); got == shown || got == "" {
		t.Fatalf("bugün sabitlenen içerik dönüşte kaldı: %q", got)
	}
}

func TestManage(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	mod := e.member(t, tf, authz.Moderator)
	member := e.member(t, tf, authz.Member)
	stranger := id.New()

	_, err := e.svc.Create(ctx, member, tf, daily.Input{Body: "x"}, id.New().String())
	wantErr(t, "üye ekleme", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Today(ctx, stranger, tf)
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.List(ctx, member, tf, true)
	wantErr(t, "üye silinenler", err, apperr.ErrPermissionDenied)

	// Validation.
	for field, in := range map[string]daily.Input{
		daily.FieldBody:        {Body: "bir\x00iki"},
		daily.FieldAttribution: {Body: "x", Attribution: strings.Repeat("a", 121)},
		daily.FieldPin:         {Body: "x", Pin: &daily.Pin{Month: 2, Day: 30}},
	} {
		_, err := e.svc.Create(ctx, mod, tf, in, id.New().String())
		wantField(t, field, err, field)
	}
	_, err = e.svc.Create(ctx, mod, tf, daily.Input{Body: "x"}, "x")
	wantField(t, "istek kimliği", err, daily.FieldClientRequestID)

	// A poem keeps its lines; a retry returns the same item.
	crid := id.New().String()
	poem, err := e.svc.Create(ctx, mod, tf, daily.Input{Body: "Öğretmen  \r\n\r\n\r\nyol gösterir", Attribution: " Şair "}, crid)
	must(t, err)
	if poem.Item.Body != "Öğretmen\n\nyol gösterir" || poem.Item.Attribution != "Şair" || poem.Item.CreatedBy != mod || poem.Item.Position != 0 {
		t.Fatalf("şiir: %+v", poem.Item)
	}
	again, err := e.svc.Create(ctx, mod, tf, daily.Input{Body: "başka"}, crid)
	must(t, err)
	if again.Item.ID != poem.Item.ID {
		t.Fatal("tekrar gönderim yeni içerik ekledi")
	}
	b := e.add(t, admin, tf, "B", nil)
	c := e.add(t, admin, tf, "C", nil)

	l, err := e.svc.List(ctx, member, tf, false)
	must(t, err)
	if len(l.Items) != 3 || l.CallerCanManage || l.Items[0].ID != poem.Item.ID {
		t.Fatalf("üye listesi: %+v", l)
	}
	if l, _ = e.svc.List(ctx, mod, tf, false); !l.CallerCanManage {
		t.Fatal("moderatör yönetemiyor")
	}

	// Update: text, attribution and pin together; the place stays.
	up, err := e.svc.Update(ctx, mod, tf, b.Item.ID, daily.Input{Body: "B2", Pin: &daily.Pin{Month: 1, Day: 1}})
	must(t, err)
	if up.Item.Body != "B2" || up.Item.Attribution != "" || up.Item.Pin == nil || up.Item.Position != b.Item.Position {
		t.Fatalf("düzenleme: %+v", up.Item)
	}

	// Delete and restore: the restored item goes to the end.
	must(t, e.svc.Delete(ctx, mod, tf, b.Item.ID))
	wantErr(t, "iki kez silme", e.svc.Delete(ctx, mod, tf, b.Item.ID), apperr.ErrDailyItemNotFound)
	_, err = e.svc.Update(ctx, mod, tf, b.Item.ID, daily.Input{Body: "x"})
	wantErr(t, "silineni düzenleme", err, apperr.ErrDailyItemNotFound)
	del, err := e.svc.List(ctx, admin, tf, true)
	must(t, err)
	if len(del.Items) != 1 || del.Items[0].ID != b.Item.ID || del.Items[0].DeletedAt.IsZero() {
		t.Fatalf("silinenler: %+v", del)
	}
	res, err := e.svc.Restore(ctx, admin, tf, b.Item.ID)
	must(t, err)
	if res.Item.Position <= c.Item.Position || !res.Item.DeletedAt.IsZero() {
		t.Fatalf("geri alınan: %+v", res.Item)
	}
	if _, err := e.svc.Restore(ctx, admin, tf, b.Item.ID); err != nil {
		t.Fatalf("canlı içeriği geri alma: %v", err)
	}
	must(t, e.svc.Delete(ctx, admin, tf, c.Item.ID))
	e.clock.set(e.clock.now().Add(daily.RestoreWindow + time.Minute))
	_, err = e.svc.Restore(ctx, admin, tf, c.Item.ID)
	wantErr(t, "süresi geçen geri alma", err, apperr.ErrDailyItemNotFound)
	_, err = e.svc.Restore(ctx, admin, tf, id.New())
	wantErr(t, "bilinmeyen", err, apperr.ErrDailyItemNotFound)

	// Reorder: the whole pool exactly once.
	for name, ids := range map[string][]id.ID{
		"eksik":      {poem.Item.ID},
		"tekrar":     {poem.Item.ID, poem.Item.ID},
		"bilinmeyen": {poem.Item.ID, id.New()},
		"silinmiş":   {poem.Item.ID, c.Item.ID},
	} {
		_, err := e.svc.Reorder(ctx, mod, tf, ids)
		wantField(t, name, err, daily.FieldItemIDs)
	}
	items, err := e.svc.Reorder(ctx, mod, tf, []id.ID{b.Item.ID, poem.Item.ID})
	must(t, err)
	if len(items) != 2 || items[0].ID != b.Item.ID || items[0].Position != 0 || items[1].Position != 1 {
		t.Fatalf("sıra: %+v", items)
	}
	_, err = e.svc.Reorder(ctx, member, tf, []id.ID{b.Item.ID, poem.Item.ID})
	wantErr(t, "üye sıralama", err, apperr.ErrPermissionDenied)

	// The module off: every method is refused.
	e.exec(t, `UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'daily'`, tf.PG())
	_, err = e.svc.Today(ctx, member, tf)
	wantErr(t, "modül kapalı, bugün", err, apperr.ErrModuleDisabled)
	_, err = e.svc.Create(ctx, mod, tf, daily.Input{Body: "x"}, id.New().String())
	wantErr(t, "modül kapalı, ekleme", err, apperr.ErrModuleDisabled)
}

// The pool holds at most MaxItems live items; deleted ones do not count.
func TestLimit(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.exec(t, `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, client_request_id, created_at, updated_at)
		SELECT gen_random_uuid(), $1, 'x', g, gen_random_uuid(), now(), now() FROM generate_series(0, $2::int) AS g`,
		tf.PG(), daily.MaxItems-2)
	last := e.add(t, admin, tf, "son", nil)
	if last.Item.Position != daily.MaxItems-1 {
		t.Fatalf("yer: %d", last.Item.Position)
	}
	_, err := e.svc.Create(ctx, admin, tf, daily.Input{Body: "fazla"}, id.New().String())
	wantErr(t, "dolu havuz", err, apperr.ErrDailyItemLimit)
	must(t, e.svc.Delete(ctx, admin, tf, last.Item.ID))
	e.add(t, admin, tf, "yer açıldı", nil)
	_, err = e.svc.Restore(ctx, admin, tf, last.Item.ID)
	wantErr(t, "dolu havuza geri alma", err, apperr.ErrDailyItemLimit)
}

// The database refuses what the app would never write.
func TestChecks(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	for name, sql := range map[string]string{
		"denetim kodu":      `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, E'a\tb', 0, gen_random_uuid(), now(), now())`,
		"üç satır sonu":     `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, E'a\n\n\nb', 0, gen_random_uuid(), now(), now())`,
		"kenar boşluğu":     `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, E'a \nb', 0, gen_random_uuid(), now(), now())`,
		"30 Şubat":          `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, pin_month, pin_day, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'a', 0, 2, 30, gen_random_uuid(), now(), now())`,
		"artık olmayan yıl": `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, pin_month, pin_day, pin_year, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'a', 0, 2, 29, 2027, gen_random_uuid(), now(), now())`,
		"yarım sabitleme":   `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, pin_month, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'a', 0, 2, gen_random_uuid(), now(), now())`,
		"modül adı":         `INSERT INTO tayfa_modules (tayfa_id, module, enabled) VALUES ($1, 'weather', true)`,
	} {
		if _, err := e.admin.Exec(ctx, sql, tf.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	if _, err := e.admin.Exec(ctx, `INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, pin_month, pin_day, pin_year, client_request_id, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, E'bir\n\niki', 0, 2, 29, 2028, gen_random_uuid(), now(), now())`, tf.PG()); err != nil {
		t.Fatalf("geçerli satır: %v", err)
	}
}
