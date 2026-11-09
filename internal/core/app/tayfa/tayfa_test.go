package tayfa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	q    *store.Queries
	// shift moves the service clock; the daily limit is counted against the
	// database's created_at.
	shift time.Duration
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "core", coredb.Migrations())
	e := &env{pool: pool, q: store.New(pool)}
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Now().Add(e.shift) }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func input(name string) CreateInput {
	return CreateInput{Name: name, ClientRequestID: id.New().String()}
}

// create makes a tayfa whose admin is a new user; open tayfas let members
// join as active.
func (e *env) create(t *testing.T, policy string) (tayfa, admin id.ID) {
	t.Helper()
	admin = id.New()
	in := input("Klasik Otomobil Tayfası")
	in.JoinPolicy = policy
	v, err := e.svc.Create(context.Background(), admin, in)
	must(t, err)
	return v.ID, admin
}

// join adds user to tayfa with the given status by the user's own request.
func (e *env) join(t *testing.T, tayfa, user id.ID, status authz.Status) {
	t.Helper()
	must(t, txn.WithActor(context.Background(), e.pool, user, func(_ pgx.Tx, q *store.Queries) error {
		return q.CreateMembership(context.Background(), store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: user.PG(), Role: "member", Status: string(status),
			JoinedVia: "code", JoinedAt: tsIf(status == authz.StatusActive),
		})
	}))
}

func tsIf(ok bool) pgtype.Timestamptz {
	if !ok {
		return pgtype.Timestamptz{}
	}
	return ts(time.Now())
}

// member adds an active member of the given role.
func (e *env) member(t *testing.T, tayfa, admin id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, tayfa, u, authz.StatusActive)
	if role != authz.Member {
		must(t, txn.WithActor(context.Background(), e.pool, admin, func(_ pgx.Tx, q *store.Queries) error {
			_, err := q.SetMembershipRole(context.Background(), store.SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: u.PG(), Role: role.String()})
			return err
		}))
	}
	return u
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

var joinCodePattern = regexp.MustCompile(`^[2-9A-HJ-NP-Z]{8}$`)

func TestCreate(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := id.New()
	in := CreateInput{
		Name: "  İSTANBUL Işıklı Tayfa ", Description: " Hafta sonu kamp ",
		Interests: []string{"general", "camp", "convoy"}, JoinPolicy: JoinOpen,
		ClientRequestID: id.New().String(),
	}
	v, err := e.svc.Create(ctx, user, in)
	must(t, err)
	if v.Name != "İSTANBUL Işıklı Tayfa" || v.Description != "Hafta sonu kamp" || v.JoinPolicy != JoinOpen || v.Status != "active" {
		t.Fatalf("görünüm = %+v", v)
	}
	if !slices.Equal(v.Interests, []string{"convoy", "camp", "general"}) {
		t.Fatalf("ilgi alanları = %v", v.Interests)
	}
	if len(v.Modules) != len(Modules) {
		t.Fatalf("modüller = %v", v.Modules)
	}
	for i, m := range v.Modules {
		if m.Module != Modules[i] || !m.Enabled {
			t.Fatalf("modüller = %v", v.Modules)
		}
	}
	if v.MemberCount != 1 || !joinCodePattern.MatchString(v.JoinCode) {
		t.Fatalf("üye=%d kod biçimi=%v", v.MemberCount, joinCodePattern.MatchString(v.JoinCode))
	}

	var normalized string
	must(t, e.pool.QueryRow(ctx, "SELECT name_normalized FROM tayfas WHERE id = $1", v.ID.PG()).Scan(&normalized))
	if normalized != "istanbul ışıklı tayfa" {
		t.Fatalf("name_normalized = %q", normalized)
	}
	m, err := e.q.GetMembership(ctx, store.GetMembershipParams{TayfaID: v.ID.PG(), UserID: user.PG()})
	must(t, err)
	if m.Role != "admin" || m.Status != "active" || m.JoinedVia != "creator" || !m.JoinedAt.Valid {
		t.Fatalf("kurucu üyelik = %+v", m)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = 'tayfa.created' AND actor_id = $2", v.ID.PG(), user.PG()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE tayfa_id = $1 AND type = 'tayfa.created' AND actor_id = $2", v.ID.PG(), user.PG()); n != 1 {
		t.Fatalf("olay = %d", n)
	}

	_, role, err := e.svc.Get(ctx, user, v.ID)
	if err != nil || role != authz.Admin {
		t.Fatalf("Get: %v %v", role, err)
	}
}

func TestCreateReplayAndDailyLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := id.New()
	in := input("Kamp Tayfası")
	first, err := e.svc.Create(ctx, user, in)
	must(t, err)

	again, err := e.svc.Create(ctx, user, in)
	if err != nil || again.ID != first.ID {
		t.Fatalf("tekrar gönderim: %v %v", again.ID, err)
	}
	if n := e.count(t, "SELECT count(*) FROM tayfas WHERE created_by = $1", user.PG()); n != 1 {
		t.Fatalf("tayfa sayısı = %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'tayfa.created'"); n != 1 {
		t.Fatalf("olay sayısı = %d", n)
	}

	if _, err := e.svc.Create(ctx, user, input("İkinci Tayfa")); !errors.Is(err, apperr.ErrTayfaCreateLimit) {
		t.Fatalf("günlük sınır: %v", err)
	}
	// Another user's request ID is their own: no collision, no leak.
	other, err := e.svc.Create(ctx, id.New(), in)
	if err != nil || other.ID == first.ID {
		t.Fatalf("başka kullanıcı: %v %v", other.ID, err)
	}

	// After 24 hours the user may create again; the old request ID still
	// returns the first tayfa.
	e.shift = 25 * time.Hour
	if _, err := e.svc.Create(ctx, user, input("Ertesi Gün")); err != nil {
		t.Fatalf("24 saat sonra: %v", err)
	}
	if again, err := e.svc.Create(ctx, user, in); err != nil || again.ID != first.ID {
		t.Fatalf("eski istek: %v", err)
	}

	// A deleted tayfa still counts and its request ID cannot be reused.
	e.exec(t, "UPDATE tayfas SET deleted_at = now() WHERE id = $1", first.ID.PG())
	var v *apperr.ValidationError
	if _, err := e.svc.Create(ctx, user, in); !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Fatalf("silinmiş tayfanın isteği: %v", err)
	}
}

func TestCreateConcurrentReplay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := id.New()
	in := input("Eşzamanlı Tayfa")
	errs := make(chan error, 4)
	ids := make(chan id.ID, 4)
	for range 4 {
		go func() {
			v, err := e.svc.Create(ctx, user, in)
			errs <- err
			ids <- v.ID
		}()
	}
	var first id.ID
	for i := range 4 {
		must(t, <-errs)
		got := <-ids
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("farklı tayfalar: %v %v", first, got)
		}
	}
	if n := e.count(t, "SELECT count(*) FROM tayfas WHERE created_by = $1", user.PG()); n != 1 {
		t.Fatalf("tayfa sayısı = %d", n)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	long := strings.Repeat("ş", 51)
	for _, tc := range []struct {
		name  string
		in    CreateInput
		field string
	}{
		{"kısa ad", CreateInput{Name: " ab "}, FieldName},
		{"uzun ad", CreateInput{Name: long}, FieldName},
		{"çok uzun ad", CreateInput{Name: strings.Repeat("a", 10000)}, FieldName},
		{"kontrol karakteri", CreateInput{Name: "Tay\x00fa"}, FieldName},
		{"satır sonu adda", CreateInput{Name: "Tay\nfa"}, FieldName},
		{"geçersiz utf-8", CreateInput{Name: "Tay\xfffa"}, FieldName},
		{"uzun açıklama", CreateInput{Name: "Tayfa", Description: strings.Repeat("a", 501)}, FieldDescription},
		{"tekrar eden ilgi", CreateInput{Name: "Tayfa", Interests: []string{"camp", "camp"}}, FieldInterests},
		{"bilinmeyen ilgi", CreateInput{Name: "Tayfa", Interests: []string{"dance"}}, FieldInterests},
		{"bilinmeyen katılım", CreateInput{Name: "Tayfa", JoinPolicy: "invite"}, FieldJoinPolicy},
		{"istek kimliği yok", CreateInput{Name: "Tayfa"}, "client_request_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			if tc.field != "client_request_id" {
				in.ClientRequestID = id.New().String()
			}
			_, err := e.svc.Create(ctx, id.New(), in)
			var v *apperr.ValidationError
			if !errors.As(err, &v) || v.Field != tc.field {
				t.Fatalf("hata = %v", err)
			}
		})
	}
	if n := e.count(t, "SELECT count(*) FROM tayfas"); n != 0 {
		t.Fatalf("geçersiz istek tayfa oluşturdu: %d", n)
	}
	// Multiline descriptions and 500 characters are fine.
	in := input("Tayfa")
	in.Description = "satır\nsatır\t" + strings.Repeat("ğ", 488)
	if _, err := e.svc.Create(ctx, id.New(), in); err != nil {
		t.Fatal(err)
	}
}

// call runs every tayfa-scoped method as user and returns the errors by
// method name.
func (e *env) callAll(user, tayfa id.ID) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	_, _, out["Get"] = e.svc.Get(ctx, user, tayfa)
	_, out["Update"] = e.svc.Update(ctx, user, tayfa, UpdateInput{Fields: []string{FieldName}, Name: "Yeni Ad"})
	out["SetModule"] = e.svc.SetModule(ctx, user, tayfa, "qa", false)
	_, out["RotateJoinCode"] = e.svc.RotateJoinCode(ctx, user, tayfa)
	_, out["RequestDeletion"] = e.svc.RequestDeletion(ctx, user, tayfa)
	out["CancelDeletion"] = e.svc.CancelDeletion(ctx, user, tayfa)
	return out
}

func TestNonMembersGetNotFound(t *testing.T) {
	e := setup(t)
	tayfa, admin := e.create(t, JoinApproval)
	other, otherAdmin := e.create(t, JoinOpen)
	_ = otherAdmin

	pending, banned, invited := id.New(), id.New(), id.New()
	e.join(t, tayfa, pending, authz.StatusPending)
	e.join(t, tayfa, banned, authz.StatusPending)
	must(t, txn.WithActor(context.Background(), e.pool, admin, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := q.BanMembership(context.Background(), store.BanMembershipParams{TayfaID: tayfa.PG(), UserID: banned.PG()}); err != nil {
			return err
		}
		return q.CreateMembership(context.Background(), store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: invited.PG(), Role: "member", Status: "invited",
			JoinedVia: "direct_invite", InvitedByUserID: admin.PG(),
		})
	}))

	before := e.count(t, "SELECT count(*) FROM audit_logs")
	cases := map[string]struct{ user, tayfa id.ID }{
		"üye değil":                 {id.New(), tayfa},
		"bekleyen":                  {pending, tayfa},
		"banlı":                     {banned, tayfa},
		"davetli":                   {invited, tayfa},
		"başka tayfanın yöneticisi": {otherAdmin, tayfa},
		"bilinmeyen tayfa":          {admin, id.New()},
	}
	for name, c := range cases {
		for method, err := range e.callAll(c.user, c.tayfa) {
			if !errors.Is(err, apperr.ErrTayfaNotFound) {
				t.Errorf("%s %s: %v", name, method, err)
			}
		}
	}
	// A deleted tayfa is not found even by its admin.
	e.exec(t, "UPDATE tayfas SET deleted_at = now() WHERE id = $1", other.PG())
	for method, err := range e.callAll(otherAdmin, other) {
		if !errors.Is(err, apperr.ErrTayfaNotFound) {
			t.Errorf("silinmiş %s: %v", method, err)
		}
	}
	if after := e.count(t, "SELECT count(*) FROM audit_logs"); after != before {
		t.Fatalf("reddedilen istekler denetim kaydı yazdı: %d → %d", before, after)
	}
}

func TestRolesBelowAdminAreDenied(t *testing.T) {
	e := setup(t)
	tayfa, admin := e.create(t, JoinOpen)
	for _, role := range []authz.Role{authz.Member, authz.Moderator} {
		u := e.member(t, tayfa, admin, role)
		for method, err := range e.callAll(u, tayfa) {
			want := apperr.ErrPermissionDenied
			if method == "Get" {
				want = nil
			}
			if !errors.Is(err, want) {
				t.Errorf("%v %s: %v", role, method, err)
			}
		}
		v, r, err := e.svc.Get(context.Background(), u, tayfa)
		if err != nil || r != role || v.JoinCode != "" {
			t.Fatalf("%v: rol=%v kod gizli=%v err=%v", role, r, v.JoinCode == "", err)
		}
	}
}

func TestUpdate(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tayfa, admin := e.create(t, JoinApproval)

	v, err := e.svc.Update(ctx, admin, tayfa, UpdateInput{
		Fields: []string{FieldInterests, FieldName, FieldJoinPolicy},
		Name:   "Yeni Ad", Interests: []string{"hike"}, JoinPolicy: JoinOpen,
		Description: "maskede yok",
	})
	must(t, err)
	if v.Name != "Yeni Ad" || v.Description != "" || v.JoinPolicy != JoinOpen || !slices.Equal(v.Interests, []string{"hike"}) {
		t.Fatalf("güncel = %+v", v)
	}
	v, err = e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldDescription, FieldInterests}, Description: "Açıklama"})
	must(t, err)
	if v.Name != "Yeni Ad" || v.Description != "Açıklama" || len(v.Interests) != 0 {
		t.Fatalf("güncel = %+v", v)
	}
	var meta string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'tayfa.updated' ORDER BY id LIMIT 1").Scan(&meta))
	if meta != `{"fields": ["name", "interests", "join_policy"]}` {
		t.Fatalf("olay = %s", meta)
	}

	for _, tc := range []struct {
		in    UpdateInput
		field string
	}{
		{UpdateInput{}, "update_mask"},
		{UpdateInput{Fields: []string{"status"}}, "update_mask"},
		{UpdateInput{Fields: []string{"join_code"}}, "update_mask"},
		{UpdateInput{Fields: []string{FieldName}, Name: "a"}, FieldName},
		{UpdateInput{Fields: []string{FieldJoinPolicy}}, FieldJoinPolicy},
		{UpdateInput{Fields: []string{FieldInterests}, Interests: []string{"x"}}, FieldInterests},
	} {
		var ve *apperr.ValidationError
		if _, err := e.svc.Update(ctx, admin, tayfa, tc.in); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%+v: %v", tc.in, err)
		}
	}
}

func TestSetModuleAndRotateJoinCode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tayfa, admin := e.create(t, JoinApproval)
	audits := func() int { return e.count(t, "SELECT count(*) FROM audit_logs WHERE tayfa_id = $1", tayfa.PG()) }

	start := audits()
	must(t, e.svc.SetModule(ctx, admin, tayfa, "qa", true)) // already on
	if audits() != start {
		t.Fatal("değişmeyen modül kayıt yazdı")
	}
	must(t, e.svc.SetModule(ctx, admin, tayfa, "qa", false))
	v, _, err := e.svc.Get(ctx, admin, tayfa)
	must(t, err)
	for _, m := range v.Modules {
		if m.Enabled != (m.Module != "qa") {
			t.Fatalf("modüller = %v", v.Modules)
		}
	}
	if audits() != start+1 {
		t.Fatal("modül değişikliği kaydedilmedi")
	}
	var ve *apperr.ValidationError
	if err := e.svc.SetModule(ctx, admin, tayfa, "chat", false); !errors.As(err, &ve) {
		t.Fatalf("bilinmeyen modül: %v", err)
	}

	old := v.JoinCode
	code, err := e.svc.RotateJoinCode(ctx, admin, tayfa)
	must(t, err)
	if code == old || !joinCodePattern.MatchString(code) {
		t.Fatal("yeni kod geçersiz")
	}
	if _, err := e.q.GetTayfaByJoinCode(ctx, old); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("eski kod hâlâ çalışıyor: %v", err)
	}
	if _, err := e.q.GetTayfaByJoinCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%' OR metadata::text LIKE '%' || $2 || '%'", code, old); n != 0 {
		t.Fatal("katılım kodu denetim kaydına yazıldı")
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE payload::text LIKE '%' || $1 || '%'", code); n != 0 {
		t.Fatal("katılım kodu olaya yazıldı")
	}
}

func TestDeletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tayfa, admin := e.create(t, JoinOpen)
	admin2 := e.member(t, tayfa, admin, authz.Admin)

	if err := e.svc.CancelDeletion(ctx, admin, tayfa); !errors.Is(err, apperr.ErrDeletionNotRequested) {
		t.Fatalf("talep yokken iptal: %v", err)
	}
	before := time.Now().Truncate(time.Microsecond)
	at, err := e.svc.RequestDeletion(ctx, admin, tayfa)
	must(t, err)
	if d := at.Sub(before); d < DeletionDelay || d > DeletionDelay+time.Minute {
		t.Fatalf("silme zamanı = %v", d)
	}
	v, _, err := e.svc.Get(ctx, admin2, tayfa)
	if err != nil || !v.DeletionAt.Equal(at) {
		t.Fatalf("Get: %v %v", v.DeletionAt, err)
	}
	if _, err := e.svc.RequestDeletion(ctx, admin2, tayfa); !errors.Is(err, apperr.ErrDeletionAlreadyRequested) {
		t.Fatalf("ikinci talep: %v", err)
	}
	// Read-only while waiting.
	if _, err := e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldName}, Name: "Yeni Ad"}); !errors.Is(err, apperr.ErrTayfaReadOnly) {
		t.Fatalf("beklerken güncelleme: %v", err)
	}
	if err := e.svc.SetModule(ctx, admin, tayfa, "qa", false); !errors.Is(err, apperr.ErrTayfaReadOnly) {
		t.Fatalf("beklerken modül: %v", err)
	}
	if _, err := e.svc.RotateJoinCode(ctx, admin, tayfa); !errors.Is(err, apperr.ErrTayfaReadOnly) {
		t.Fatalf("beklerken kod: %v", err)
	}
	// Any admin may cancel.
	must(t, e.svc.CancelDeletion(ctx, admin2, tayfa))
	v, _, err = e.svc.Get(ctx, admin, tayfa)
	if err != nil || !v.DeletionAt.IsZero() {
		t.Fatalf("iptal sonrası: %v %v", v.DeletionAt, err)
	}
	if _, err := e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldName}, Name: "Yeni Ad"}); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{EventDeletionRequested, EventDeletionCancelled} {
		if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = $1 AND tayfa_id = $2", typ, tayfa.PG()); n != 1 {
			t.Fatalf("%s olayı = %d", typ, n)
		}
	}
}

func TestSuspendedAndArchivedAreReadOnly(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for name, sql := range map[string]string{
		"askıda":   "UPDATE tayfas SET suspended_at = now(), suspended_reason = 'kural ihlali' WHERE id = $1",
		"arşivlik": "UPDATE tayfas SET status = 'archived', archived_at = now() WHERE id = $1",
	} {
		tayfa, admin := e.create(t, JoinApproval)
		e.exec(t, sql, tayfa.PG())
		for method, err := range e.callAll(admin, tayfa) {
			var want error
			switch method {
			case "Get":
			case "CancelDeletion":
				want = apperr.ErrDeletionNotRequested
			default:
				want = apperr.ErrTayfaReadOnly
			}
			if !errors.Is(err, want) {
				t.Errorf("%s %s: %v", name, method, err)
			}
		}
		v, _, err := e.svc.Get(ctx, admin, tayfa)
		must(t, err)
		if name == "askıda" && v.SuspendedAt.IsZero() {
			t.Error("askı zamanı dönmedi")
		}
		if name == "arşivlik" && v.Status != "archived" {
			t.Error("durum dönmedi")
		}
	}
}

func TestListMine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := id.New()

	var want []id.ID // most recent first
	for range 3 {
		tayfa, _ := e.create(t, JoinOpen)
		e.join(t, tayfa, user, authz.StatusActive)
		want = append([]id.ID{tayfa}, want...)
	}
	pendingTayfa, _ := e.create(t, JoinApproval)
	e.join(t, pendingTayfa, user, authz.StatusPending)
	want = append([]id.ID{pendingTayfa}, want...)
	// Banned and deleted tayfas are not listed.
	bannedTayfa, bannedAdmin := e.create(t, JoinOpen)
	e.join(t, bannedTayfa, user, authz.StatusActive)
	must(t, txn.WithActor(ctx, e.pool, bannedAdmin, func(_ pgx.Tx, q *store.Queries) error {
		_, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: bannedTayfa.PG(), UserID: user.PG()})
		return err
	}))
	deletedTayfa, _ := e.create(t, JoinOpen)
	e.join(t, deletedTayfa, user, authz.StatusActive)
	e.exec(t, "UPDATE tayfas SET deleted_at = now() WHERE id = $1", deletedTayfa.PG())
	// Someone else's tayfas never appear.
	e.create(t, JoinOpen)

	var got []MyTayfa
	token := ""
	for page := 0; ; page++ {
		list, next, err := e.svc.ListMine(ctx, user, 3, token)
		must(t, err)
		got = append(got, list...)
		if next == "" {
			break
		}
		if page > 3 {
			t.Fatal("sayfalama bitmiyor")
		}
		token = next
	}
	if len(got) != len(want) {
		t.Fatalf("liste = %d, beklenen %d", len(got), len(want))
	}
	for i, g := range got {
		if g.TayfaID != want[i] || g.Name == "" {
			t.Fatalf("%d: %v", i, g.TayfaID)
		}
		if g.TayfaID == pendingTayfa {
			if g.Status != authz.StatusPending || g.Tayfa != nil {
				t.Fatalf("bekleyen = %+v", g)
			}
			continue
		}
		if g.Status != authz.StatusActive || g.Tayfa == nil || g.Tayfa.MemberCount != 2 || g.Tayfa.JoinCode != "" || len(g.Tayfa.Modules) != len(Modules) {
			t.Fatalf("aktif = %+v", g)
		}
	}

	for _, tc := range []struct {
		size  int32
		token string
		field string
	}{
		{-1, "", "page_size"},
		{0, "bozuk!", "page_token"},
		{0, "bXkxLjEuMg", "page_token"},
		{0, strings.Repeat("a", 200), "page_token"},
	} {
		var ve *apperr.ValidationError
		if _, _, err := e.svc.ListMine(ctx, user, tc.size, tc.token); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%d %q: %v", tc.size, tc.token, err)
		}
	}
	list, next, err := e.svc.ListMine(ctx, user, 1000, "")
	if err != nil || len(list) != len(want) || next != "" {
		t.Fatalf("büyük sayfa: %d %q %v", len(list), next, err)
	}
	list, _, err = e.svc.ListMine(ctx, id.New(), 0, "")
	if err != nil || len(list) != 0 {
		t.Fatalf("boş liste: %v %v", list, err)
	}
}

func TestJoinCodeAlphabet(t *testing.T) {
	seen := map[rune]bool{}
	for range 2000 {
		c := newJoinCode()
		if !joinCodePattern.MatchString(c) {
			t.Fatalf("kod = %q", c)
		}
		for _, r := range c {
			seen[r] = true
		}
	}
	if len(seen) != len(joinCodeAlphabet) {
		t.Fatalf("kullanılan karakter = %d", len(seen))
	}
}

func TestMembersCanCreateEventsSetting(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tayfa, admin := e.create(t, JoinOpen)
	mod := e.member(t, tayfa, admin, authz.Moderator)

	v, _, err := e.svc.Get(ctx, admin, tayfa)
	must(t, err)
	if v.MembersCanCreateEvents {
		t.Fatal("yeni tayfada ayar açık")
	}

	// Moderators cannot change it; only admins update settings.
	if _, err := e.svc.Update(ctx, mod, tayfa, UpdateInput{Fields: []string{FieldMembersCanCreateEvents}, MembersCanCreateEvents: true}); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("moderatör ayarı değiştirdi: %v", err)
	}

	v, err = e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
	if !v.MembersCanCreateEvents {
		t.Fatal("ayar açılmadı")
	}
	// Updating another field keeps the setting.
	v, err = e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldName}, Name: "Başka Ad"})
	must(t, err)
	if !v.MembersCanCreateEvents {
		t.Fatal("başka alan güncellenince ayar kapandı")
	}
	// Outside the mask the value is ignored.
	v, err = e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldDescription}, Description: "x"})
	must(t, err)
	if !v.MembersCanCreateEvents {
		t.Fatal("maskede olmayan ayar değişti")
	}
	v, err = e.svc.Update(ctx, admin, tayfa, UpdateInput{Fields: []string{FieldMembersCanCreateEvents}})
	must(t, err)
	if v.MembersCanCreateEvents {
		t.Fatal("ayar kapanmadı")
	}

	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'tayfa.updated' ORDER BY id LIMIT 1").Scan(&payload))
	if payload != `{"fields": ["members_can_create_events"]}` {
		t.Fatalf("olay = %s", payload)
	}

	// The list view carries it too.
	list, _, err := e.svc.ListMine(ctx, admin, 0, "")
	must(t, err)
	if len(list) != 1 || list[0].Tayfa == nil || list[0].Tayfa.MembersCanCreateEvents {
		t.Fatalf("liste = %+v", list)
	}
}
