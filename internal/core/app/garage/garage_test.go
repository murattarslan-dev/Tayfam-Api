package garage_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/garage"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func TestCleanText(t *testing.T) {
	for _, tc := range [][2]string{
		{"  Anadol   A1 ", "Anadol A1"},
		{"Kırmızı\nbeyaz\t", "Kırmızı beyaz"},
		{"", ""},
		{strings.Repeat("ş", garage.NoteMax), strings.Repeat("ş", garage.NoteMax)},
	} {
		if got, ok := garage.CleanText(tc[0], garage.NoteMax); !ok || got != tc[1] {
			t.Errorf("CleanText(%q) = %q %v, %q bekleniyordu", tc[0], got, ok, tc[1])
		}
	}
	for name, in := range map[string]string{
		"denetim kodu":    "Anadol\x00",
		"görünmez işaret": "Ana\u200bdol",
		"yön işareti":     "Anadol\u202e",
		"geçersiz UTF-8":  "Ana\xffdol",
		"uzun":            strings.Repeat("ş", garage.NoteMax+1),
	} {
		if _, ok := garage.CleanText(in, garage.NoteMax); ok {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

type env struct {
	svc    *garage.Service
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
	e := &env{pool: d.App, admin: d.Admin, now: time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: func() time.Time { return e.now }})
	must(t, err)
	e.svc = garage.New(fw)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
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

func (e *env) newTayfa(t *testing.T, interests ...string) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Klasik Tayfa", Interests: interests, ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

func (e *env) member(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, tf, u, role)
	return u
}

func (e *env) join(t *testing.T, tf, u id.ID, role authz.Role) {
	t.Helper()
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
}

func (e *env) photo(t *testing.T, owner id.ID) id.ID {
	t.Helper()
	return mediatest.Ready(t, e.pool, owner, media.PurposeGarageVehicle, id.ID{})
}

func (e *env) add(t *testing.T, owner id.ID, in garage.Input) garage.Vehicle {
	t.Helper()
	v, err := e.svc.Create(ctx, owner, in, id.New().String())
	must(t, err)
	return v
}

func ids(vs []garage.Vehicle) []id.ID {
	out := make([]id.ID, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.admin.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// The garage keeps no plate (ADR-0040 §4): no column of the garage tables
// is a plate, the export has none, and the event's plate never reaches the
// garage.
func TestNoPlateInGarage(t *testing.T) {
	e := setup(t)
	if n := e.count(t, `SELECT count(*) FROM information_schema.columns
		WHERE table_name IN ('garage_vehicles', 'garage_tayfa_visibility') AND column_name ILIKE '%plate%'`); n != 0 {
		t.Fatalf("garaj tablolarında %d plaka kolonu var", n)
	}
	if n := e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'garage_vehicles'`); n == 0 {
		t.Fatal("garage_vehicles yok")
	}
	owner := id.New()
	e.add(t, owner, garage.Input{Make: "Anadol", Note: "Hafta sonu çıkar"})
	rows, err := store.New(e.pool).ExportGarageVehicles(ctx, store.ExportGarageVehiclesParams{UserID: owner.PG(), After: id.ID{}.PG(), N: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("dışa aktarma: %d satır", len(rows))
	}
	var data map[string]any
	must(t, json.Unmarshal(rows[0].Data, &data))
	for k := range data {
		if strings.Contains(k, "plate") {
			t.Fatalf("dışa aktarmada plaka alanı: %v", data)
		}
	}
	if data["make"] != "Anadol" || data["note"] != "Hafta sonu çıkar" {
		t.Fatalf("dışa aktarma: %v", data)
	}
}

// The owner's garage: added, listed in order, edited, deleted; retries,
// the limit, other people's vehicles and bad input.
func TestOwnGarage(t *testing.T) {
	e := setup(t)
	owner, other := id.New(), id.New()

	p1, p2 := e.photo(t, owner), e.photo(t, owner)
	crid := id.New().String()
	a, err := e.svc.Create(ctx, owner, garage.Input{
		Make: "  Anadol ", Model: "A1", ModelYear: 1968, Color: "Kırmızı", Note: "Babamdan  kaldı", Photos: []id.ID{p2, p1},
	}, crid)
	must(t, err)
	if a.Make != "Anadol" || a.Model != "A1" || a.ModelYear != 1968 || a.Color != "Kırmızı" || a.Note != "Babamdan kaldı" ||
		!slices.Equal(a.Photos, []id.ID{p2, p1}) || a.Owner != owner || !a.CallerCanEdit {
		t.Fatalf("araç: %+v", a)
	}
	again, err := e.svc.Create(ctx, owner, garage.Input{Make: "Başka"}, crid)
	must(t, err)
	if again.ID != a.ID || again.Make != "Anadol" {
		t.Fatalf("tekrar gönderim: %+v", again)
	}
	e.now = e.now.Add(time.Minute)
	b := e.add(t, owner, garage.Input{Make: "Murat", Model: "124"})
	mine, err := e.svc.Mine(ctx, owner)
	must(t, err)
	if !slices.Equal(ids(mine.Vehicles), []id.ID{a.ID, b.ID}) || !mine.CallerCanAdd {
		t.Fatalf("garaj: %+v", mine)
	}
	theirs, err := e.svc.Mine(ctx, other)
	must(t, err)
	if len(theirs.Vehicles) != 0 {
		t.Fatalf("başkasının garajı: %+v", theirs)
	}

	// Another person's photo, a photo of another purpose, a used photo.
	_, err = e.svc.Create(ctx, owner, garage.Input{Make: "Ford", Photos: []id.ID{e.photo(t, other)}}, id.New().String())
	wantErr(t, "başkasının fotoğrafı", err, apperr.ErrUploadNotFound)
	tf, _ := e.newTayfa(t)
	cover := mediatest.Ready(t, e.pool, owner, media.PurposeMaterialNote, tf)
	_, err = e.svc.Create(ctx, owner, garage.Input{Make: "Ford", Photos: []id.ID{cover}}, id.New().String())
	wantErr(t, "başka amaçlı fotoğraf", err, apperr.ErrUploadNotFound)
	_, err = e.svc.Create(ctx, owner, garage.Input{Make: "Ford", Photos: []id.ID{p1}}, id.New().String())
	wantErr(t, "kullanılmış fotoğraf", err, apperr.ErrUploadInUse)

	year := int32(e.now.Year())
	for _, tc := range []struct {
		name  string
		in    garage.Input
		field string
	}{
		{"marka yok", garage.Input{}, garage.FieldMake},
		{"boşluk marka", garage.Input{Make: "  \t"}, garage.FieldMake},
		{"uzun marka", garage.Input{Make: strings.Repeat("m", garage.MakeMax+1)}, garage.FieldMake},
		{"denetim karakterli marka", garage.Input{Make: "Ana\x07dol"}, garage.FieldMake},
		{"uzun model", garage.Input{Make: "Ford", Model: strings.Repeat("m", garage.ModelMax+1)}, garage.FieldModel},
		{"uzun renk", garage.Input{Make: "Ford", Color: strings.Repeat("r", garage.ColorMax+1)}, garage.FieldColor},
		{"uzun not", garage.Input{Make: "Ford", Note: strings.Repeat("n", garage.NoteMax+1)}, garage.FieldNote},
		{"görünmez işaretli not", garage.Input{Make: "Ford", Note: "a\u200bb"}, garage.FieldNote},
		{"1899 model", garage.Input{Make: "Ford", ModelYear: 1899}, garage.FieldModelYear},
		{"iki yıl sonrası", garage.Input{Make: "Ford", ModelYear: year + 2}, garage.FieldModelYear},
		{"dört fotoğraf", garage.Input{Make: "Ford", Photos: []id.ID{id.New(), id.New(), id.New(), id.New()}}, garage.FieldPhotoIDs},
		{"aynı fotoğraf iki kez", garage.Input{Make: "Ford", Photos: []id.ID{p1, p1}}, garage.FieldPhotoIDs},
	} {
		_, err := e.svc.Create(ctx, owner, tc.in, id.New().String())
		wantField(t, tc.name, err, tc.field)
	}
	_, err = e.svc.Create(ctx, owner, garage.Input{Make: "Ford"}, "istek")
	wantField(t, "istek kimliği", err, garage.FieldClientRequestID)
	if _, err := e.svc.Create(ctx, owner, garage.Input{Make: "Ford", ModelYear: year + 1}, id.New().String()); err != nil {
		t.Fatalf("gelecek yılın modeli: %v", err)
	}

	// Edit: the photos change order, a dropped one is released.
	p3 := e.photo(t, owner)
	u, err := e.svc.Update(ctx, owner, a.ID, garage.Input{Make: "Anadol", Model: "A1 Spor", Photos: []id.ID{p1, p3}})
	must(t, err)
	if u.Model != "A1 Spor" || u.ModelYear != 0 || u.Color != "" || u.Note != "" || !slices.Equal(u.Photos, []id.ID{p1, p3}) {
		t.Fatalf("düzenleme: %+v", u)
	}
	if mediatest.Status(t, e.pool, p2) == media.StatusReady || mediatest.Status(t, e.pool, p1) != media.StatusReady {
		t.Fatal("bırakılan fotoğraf silinmeye girmedi ya da kalan fotoğraf gitti")
	}
	_, err = e.svc.Update(ctx, other, a.ID, garage.Input{Make: "Ford"})
	wantErr(t, "başkasının aracını düzenleme", err, apperr.ErrGarageVehicleNotFound)
	err = e.svc.Delete(ctx, other, a.ID)
	wantErr(t, "başkasının aracını silme", err, apperr.ErrGarageVehicleNotFound)

	// Delete: the photos go at once; the vehicle is gone for the owner.
	must(t, e.svc.Delete(ctx, owner, a.ID))
	if mediatest.Status(t, e.pool, p1) == media.StatusReady || mediatest.Status(t, e.pool, p3) == media.StatusReady {
		t.Fatal("silinen aracın fotoğrafları kaldı")
	}
	err = e.svc.Delete(ctx, owner, a.ID)
	wantErr(t, "iki kez silme", err, apperr.ErrGarageVehicleNotFound)
	_, err = e.svc.Update(ctx, owner, a.ID, garage.Input{Make: "Anadol"})
	wantErr(t, "silineni düzenleme", err, apperr.ErrGarageVehicleNotFound)

	// The limit counts live vehicles only.
	mine, err = e.svc.Mine(ctx, owner)
	must(t, err)
	for i := len(mine.Vehicles); i < garage.MaxVehicles; i++ {
		e.add(t, owner, garage.Input{Make: "Araç"})
	}
	mine, err = e.svc.Mine(ctx, owner)
	must(t, err)
	if len(mine.Vehicles) != garage.MaxVehicles || mine.CallerCanAdd {
		t.Fatalf("dolu garaj: %d %v", len(mine.Vehicles), mine.CallerCanAdd)
	}
	_, err = e.svc.Create(ctx, owner, garage.Input{Make: "Fazla"}, id.New().String())
	wantErr(t, "on birinci araç", err, apperr.ErrGarageLimit)
}

// The garage in a tayfa: shown by default only with the automotive
// interest, the owner's choice otherwise; only active members' vehicles,
// only to active members, with the module on.
func TestTayfaVisibility(t *testing.T) {
	e := setup(t)
	auto, _ := e.newTayfa(t, "automotive")
	plain, _ := e.newTayfa(t, "nature")
	a := e.member(t, auto, authz.Member)
	b := e.member(t, auto, authz.Member)
	e.join(t, plain, a, authz.Member)
	e.join(t, plain, b, authz.Member)
	va := e.add(t, a, garage.Input{Make: "Anadol"})
	e.now = e.now.Add(time.Minute)
	vb := e.add(t, b, garage.Input{Make: "Murat"})

	list := func(user, tf id.ID, f garage.Filter) garage.Page {
		t.Helper()
		p, err := e.svc.List(ctx, user, tf, f)
		must(t, err)
		return p
	}
	if p := list(a, auto, garage.Filter{}); !slices.Equal(ids(p.Vehicles), []id.ID{vb.ID, va.ID}) || !p.CallerVisible {
		t.Fatalf("otomobil tayfası: %+v", p)
	}
	if p := list(a, plain, garage.Filter{}); len(p.Vehicles) != 0 || p.CallerVisible {
		t.Fatalf("otomobilsiz tayfa: %+v", p)
	}
	p := list(b, auto, garage.Filter{})
	if v := p.Vehicles[1]; v.CallerCanEdit || v.CallerCanRemove || !p.Vehicles[0].CallerCanEdit {
		t.Fatalf("üyenin bayrakları: %+v", p.Vehicles)
	}

	// The owner's choice per tayfa.
	shown, err := e.svc.SetVisibility(ctx, a, plain, true)
	must(t, err)
	if !shown {
		t.Fatal("gösterme")
	}
	_, err = e.svc.SetVisibility(ctx, b, auto, false)
	must(t, err)
	if p := list(b, plain, garage.Filter{}); !slices.Equal(ids(p.Vehicles), []id.ID{va.ID}) || p.CallerVisible {
		t.Fatalf("seçilen tayfa: %+v", p)
	}
	if p := list(a, auto, garage.Filter{}); !slices.Equal(ids(p.Vehicles), []id.ID{va.ID}) {
		t.Fatalf("gizlenen garaj: %+v", p)
	}

	// The owner filter and pages.
	e.now = e.now.Add(time.Minute)
	va2 := e.add(t, a, garage.Input{Make: "Tofaş"})
	first := list(b, auto, garage.Filter{Owner: a, PageSize: 1})
	if !slices.Equal(ids(first.Vehicles), []id.ID{va2.ID}) || first.NextPageToken == "" {
		t.Fatalf("ilk sayfa: %+v", first)
	}
	second := list(b, auto, garage.Filter{Owner: a, PageSize: 1, PageToken: first.NextPageToken})
	if !slices.Equal(ids(second.Vehicles), []id.ID{va.ID}) || second.NextPageToken != "" {
		t.Fatalf("ikinci sayfa: %+v", second)
	}
	_, err = e.svc.List(ctx, b, auto, garage.Filter{PageToken: first.NextPageToken})
	wantField(t, "başka süzgecin sayfası", err, garage.FieldPageToken)
	_, err = e.svc.List(ctx, b, plain, garage.Filter{Owner: a, PageToken: first.NextPageToken})
	wantField(t, "başka tayfanın sayfası", err, garage.FieldPageToken)
	_, err = e.svc.List(ctx, b, auto, garage.Filter{PageSize: garage.MaxPageSize + 1})
	wantField(t, "büyük sayfa", err, garage.FieldPageSize)

	// A muted member hides but does not show.
	e.exec(t, "UPDATE memberships SET muted_until = $3 WHERE tayfa_id = $1 AND user_id = $2", auto.PG(), b.PG(), e.now.Add(time.Hour))
	_, err = e.svc.SetVisibility(ctx, b, auto, true)
	wantErr(t, "susturulmuşken gösterme", err, apperr.ErrMuted)
	_, err = e.svc.SetVisibility(ctx, b, auto, false)
	must(t, err)

	// An owner who is no longer an active member is not shown.
	e.exec(t, "UPDATE memberships SET status = 'banned' WHERE tayfa_id = $1 AND user_id = $2", auto.PG(), a.PG())
	if p := list(e.member(t, auto, authz.Member), auto, garage.Filter{}); len(p.Vehicles) != 0 {
		t.Fatalf("üyeliği bitenin araçları: %+v", p)
	}

	// Not a member; the module off.
	_, err = e.svc.List(ctx, id.New(), auto, garage.Filter{})
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.SetVisibility(ctx, id.New(), auto, true)
	wantErr(t, "üye olmayanın görünürlüğü", err, apperr.ErrTayfaNotFound)
	e.exec(t, "UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'garage'", plain.PG())
	_, err = e.svc.List(ctx, b, plain, garage.Filter{})
	wantErr(t, "modül kapalı", err, apperr.ErrModuleDisabled)
	_, err = e.svc.SetVisibility(ctx, b, plain, true)
	wantErr(t, "modül kapalıyken görünürlük", err, apperr.ErrModuleDisabled)
}

// A garage vehicle's photo (issue #272): the owner, and the members of a
// tayfa the vehicle shows in with the module on; nobody else.
func TestPhotoSeenBy(t *testing.T) {
	e := setup(t)
	auto, _ := e.newTayfa(t, "automotive")
	plain, _ := e.newTayfa(t)
	owner := e.member(t, auto, authz.Member)
	e.join(t, plain, owner, authz.Member)
	fellow, stranger := e.member(t, auto, authz.Member), e.member(t, plain, authz.Member)
	v := e.add(t, owner, garage.Input{Make: "Anadol", Photos: []id.ID{e.photo(t, owner)}})
	seen := func(viewer id.ID) bool {
		t.Helper()
		var ok bool
		must(t, e.admin.QueryRow(ctx, "SELECT garage_vehicle_seen_by($1, $2)", v.ID.PG(), viewer.PG()).Scan(&ok))
		return ok
	}
	if !seen(fellow) || seen(stranger) || seen(id.New()) {
		t.Fatalf("görünürlük: üye %v, başka tayfa %v", seen(fellow), seen(stranger))
	}
	e.exec(t, "UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'garage'", auto.PG())
	if seen(fellow) {
		t.Fatal("modül kapalıyken görünüyor")
	}
}

// A moderator removes a vehicle that shows in the tayfa; it is hidden
// everywhere and purged later. A member cannot; a vehicle that does not
// show in the tayfa is not found.
func TestRemove(t *testing.T) {
	e := setup(t)
	auto, admin := e.newTayfa(t, "automotive")
	plain, plainAdmin := e.newTayfa(t)
	owner := e.member(t, auto, authz.Member)
	e.join(t, plain, owner, authz.Member)
	mod, member := e.member(t, auto, authz.Moderator), e.member(t, auto, authz.Member)
	v := e.add(t, owner, garage.Input{Make: "Anadol", Photos: []id.ID{e.photo(t, owner)}})

	p, err := e.svc.List(ctx, mod, auto, garage.Filter{})
	must(t, err)
	if len(p.Vehicles) != 1 || !p.Vehicles[0].CallerCanRemove {
		t.Fatalf("moderatörün bayrağı: %+v", p.Vehicles)
	}
	err = e.svc.Remove(ctx, member, auto, v.ID)
	wantErr(t, "üyenin kaldırması", err, apperr.ErrPermissionDenied)
	err = e.svc.Remove(ctx, plainAdmin, plain, v.ID)
	wantErr(t, "görünmediği tayfada kaldırma", err, apperr.ErrGarageVehicleNotFound)
	must(t, e.svc.Remove(ctx, admin, auto, v.ID))
	err = e.svc.Remove(ctx, mod, auto, v.ID)
	wantErr(t, "iki kez kaldırma", err, apperr.ErrGarageVehicleNotFound)
	mine, err := e.svc.Mine(ctx, owner)
	must(t, err)
	if len(mine.Vehicles) != 0 {
		t.Fatalf("kaldırılan araç sahibinde görünüyor: %+v", mine)
	}
	if n := e.count(t, "SELECT count(*) FROM garage_vehicles WHERE id = $1 AND deleted_by = $2", v.ID.PG(), admin.PG()); n != 1 {
		t.Fatal("kaldırma yazılmadı")
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = $2 AND target_id = $3", auto.PG(), garage.AuditRemoved, v.ID.PG()); n != 1 {
		t.Fatal("denetim kaydı yok")
	}

	// Purged after 30 days.
	n, err := store.New(e.pool).PurgeGarageVehicles(ctx, store.PurgeGarageVehiclesParams{
		DeletedBefore: pgtype.Timestamptz{Time: e.now.Add(time.Hour), Valid: true}, N: 10,
	})
	must(t, err)
	if n != 1 || e.count(t, "SELECT count(*) FROM garage_vehicles WHERE id = $1", v.ID.PG()) != 0 {
		t.Fatalf("kalıcı silme: %d", n)
	}
}

// Account deletion (KVKK): the person's vehicles and choices go; as the
// remover of someone else's vehicle the person is cleared.
func TestAccountDeletion(t *testing.T) {
	e := setup(t)
	auto, _ := e.newTayfa(t, "automotive")
	owner, mod := e.member(t, auto, authz.Member), e.member(t, auto, authz.Moderator)
	photo := e.photo(t, owner)
	e.add(t, owner, garage.Input{Make: "Anadol", Photos: []id.ID{photo}})
	_, err := e.svc.SetVisibility(ctx, owner, auto, false)
	must(t, err)
	other := e.add(t, mod, garage.Input{Make: "Murat"})
	e.exec(t, "UPDATE garage_vehicles SET deleted_at = now(), deleted_by = $1 WHERE id = $2", owner.PG(), other.ID.PG())

	q := store.New(e.pool)
	for _, clear := range []func(context.Context, pgtype.UUID) error{
		q.DeleteUserGarageVehicles, q.ClearGarageVehicleDeleter, q.DeleteUserGarageVisibility,
	} {
		must(t, clear(ctx, owner.PG()))
	}
	if n := e.count(t, "SELECT count(*) FROM garage_vehicles WHERE owner_id = $1", owner.PG()); n != 0 {
		t.Fatalf("%d araç kaldı", n)
	}
	if n := e.count(t, "SELECT count(*) FROM garage_tayfa_visibility WHERE user_id = $1", owner.PG()); n != 0 {
		t.Fatal("görünürlük seçimi kaldı")
	}
	if n := e.count(t, "SELECT count(*) FROM garage_vehicles WHERE deleted_by = $1", owner.PG()); n != 0 {
		t.Fatal("kaldıran olarak kaldı")
	}
	if mediatest.Status(t, e.pool, photo) == media.StatusReady {
		t.Fatal("fotoğraf silinmeye girmedi")
	}
}

// ForEvent copies only from the caller's own, live garage vehicle.
func TestForEvent(t *testing.T) {
	e := setup(t)
	owner := id.New()
	v := e.add(t, owner, garage.Input{Make: "Anadol", Model: "A1", ModelYear: 1968, Color: "Kırmızı"})
	q := store.New(e.pool)
	c, err := garage.ForEvent(ctx, q, owner, v.ID)
	must(t, err)
	if c != (garage.Copy{Make: "Anadol", Model: "A1", ModelYear: 1968, Color: "Kırmızı"}) {
		t.Fatalf("kopya: %+v", c)
	}
	_, err = garage.ForEvent(ctx, q, id.New(), v.ID)
	wantErr(t, "başkasının aracı", err, apperr.ErrGarageVehicleNotFound)
	must(t, e.svc.Delete(ctx, owner, v.ID))
	_, err = garage.ForEvent(ctx, q, owner, v.ID)
	wantErr(t, "silinmiş araç", err, apperr.ErrGarageVehicleNotFound)
}

// The CHECKs are a second line behind the app's checks.
func TestChecks(t *testing.T) {
	e := setup(t)
	owner := id.New()
	v := e.add(t, owner, garage.Input{Make: "Anadol"})
	for name, sql := range map[string]string{
		"boş marka":       `UPDATE garage_vehicles SET make = '' WHERE id = $1`,
		"çift boşluk":     `UPDATE garage_vehicles SET make = 'Ana  dol' WHERE id = $1`,
		"satır sonu":      "UPDATE garage_vehicles SET note = E'a\\nb' WHERE id = $1",
		"uzun not":        `UPDATE garage_vehicles SET note = repeat('n', 201) WHERE id = $1`,
		"1899 model":      `UPDATE garage_vehicles SET model_year = 1899 WHERE id = $1`,
		"silmeden silen":  `UPDATE garage_vehicles SET deleted_by = owner_id WHERE id = $1`,
		"sahibi değişmez": `UPDATE garage_vehicles SET owner_id = gen_random_uuid() WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, v.ID.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}
