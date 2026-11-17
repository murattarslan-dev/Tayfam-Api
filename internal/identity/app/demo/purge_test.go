package demo

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// realUser registers a Google account with subject as its Google subject.
func (e *env) realUser(t *testing.T, subject string) id.ID {
	t.Helper()
	ctx := context.Background()
	u := id.New()
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.CreateUser(ctx, store.CreateUserParams{ID: u.PG(), DisplayName: pgtype.Text{String: "Gerçek Kişi", Valid: true}, Locale: "tr"}); err != nil {
			return err
		}
		if err := q.CreateAuthIdentity(ctx, store.CreateAuthIdentityParams{
			ID: id.New().PG(), UserID: u.PG(), Provider: "google", Subject: subject,
			Email: pgtype.Text{String: subject + "@example.com", Valid: true},
		}); err != nil {
			return err
		}
		d, _ := birthdate.Parse("1990-01-01")
		_, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: e.cipher.Encrypt(u, d)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) demoUser(t *testing.T, subject string) string {
	t.Helper()
	var u string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT user_id::text FROM auth_identities WHERE provider = 'demo' AND subject = $1`, subject).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

// deleting reports the demo tayfas' deletion by name (not the real users'
// tayfas, whose IDs start with "real-").
func (e *env) deleting() map[string]bool {
	out := map[string]bool{}
	for k, ft := range e.fake.tayfas {
		if !strings.HasPrefix(k, "real-") {
			out[ft.name] = ft.deleting
		}
	}
	return out
}

// TestPurgeTouchesOnlyDemoData: the purge removes the Keşfet demo set by
// default and the closed set too with -hepsi, through the normal deletion
// paths; a real account (the owner's included) and a tayfa a real user
// founded, even one named like a demo tayfa or with a demo member, are
// never touched (issue #227, ADR-0035).
func TestPurgeTouchesOnlyDemoData(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	owner, code := e.owner(t)
	if _, err := e.seeder.Run(ctx, code); err != nil {
		t.Fatal(err)
	}
	// A real user named like a demo subject, with tayfas around the demo
	// founders:
	real := e.realUser(t, "demo-21")
	belgradFounder := e.demoUser(t, "demo-21")
	e.fake.mu.Lock()
	// a real user's tayfa named exactly like a demo tayfa, the demo
	// founder a member there;
	e.fake.tayfas["real-1"] = &fTayfa{id: "real-1", name: discoverTayfas[0].Name,
		roles: map[string]corev1.Role{real.String(): corev1.Role_ROLE_ADMIN, belgradFounder: corev1.Role_ROLE_MEMBER}, pending: map[string]bool{}}
	// a tayfa the demo founder admins whose name is not the data set's.
	e.fake.tayfas["real-2"] = &fTayfa{id: "real-2", name: "Başka Yürüyüş Demo",
		roles: map[string]corev1.Role{belgradFounder: corev1.Role_ROLE_ADMIN, real.String(): corev1.Role_ROLE_ADMIN}, pending: map[string]bool{}}
	e.fake.mu.Unlock()
	callsBefore := len(e.fake.calls)

	all := len(allPeople())
	demoIdentities := `SELECT count(*) FROM auth_identities WHERE provider = 'demo'`
	userDeleted := `SELECT count(*) FROM domain_events WHERE type = 'user.deleted'`

	// Dry run: counts only.
	dry, err := e.seeder.Purge(ctx, PurgeOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Tayfas) != len(discoverTayfas) || dry.Accounts != len(discoverPeople) {
		t.Fatalf("deneme raporu: %+v", dry)
	}
	for _, r := range dry.Tayfas {
		if r.Outcome != PurgeWillDelete {
			t.Errorf("deneme: %+v", r)
		}
	}
	if n := e.count(t, demoIdentities); n != all {
		t.Fatalf("deneme hesap sildi: %d", n)
	}
	for name, d := range e.deleting() {
		if d {
			t.Fatalf("deneme tayfa sildi: %s", name)
		}
	}

	// Default: only the Keşfet set.
	rep, err := e.seeder.Purge(ctx, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tayfas) != len(discoverTayfas) || rep.Accounts != len(discoverPeople) {
		t.Fatalf("rapor: %+v", rep)
	}
	del := e.deleting()
	for _, ts := range discoverTayfas {
		if !del[ts.Name] {
			t.Errorf("%s silinmedi", ts.Name)
		}
	}
	for _, ts := range tayfas {
		if del[ts.Name] {
			t.Errorf("kapalı demo tayfası %s -hepsi olmadan silindi", ts.Name)
		}
	}
	if e.fake.tayfas["real-1"].deleting || e.fake.tayfas["real-2"].deleting {
		t.Error("gerçek kullanıcının tayfasına dokunuldu")
	}
	if n := e.count(t, demoIdentities); n != len(people) {
		t.Errorf("kalan demo kimliği = %d", n)
	}
	if n := e.count(t, userDeleted); n != len(discoverPeople) {
		t.Errorf("user.deleted = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE deleted_at IS NOT NULL AND display_name IS NULL`); n != len(discoverPeople) {
		t.Errorf("kişisel verisi silinen hesap = %d", n)
	}
	for _, u := range []id.ID{owner, real} {
		if n := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NULL AND display_name IS NOT NULL AND birth_date_enc IS NOT NULL`, u.PG()); n != 1 {
			t.Errorf("gerçek hesaba dokunuldu: %s", u)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM auth_identities WHERE user_id = $1 AND provider = 'google'`, real.PG()); n != 1 {
		t.Error("gerçek hesabın giriş kimliği silindi")
	}
	// The owner's token is not used; nor is any panel or admin call.
	for _, c := range e.fake.calls[callsBefore:] {
		if c.Caller == owner.String() || c.Caller == real.String() {
			t.Errorf("gerçek hesabın token'ıyla %s çağrıldı", c.Method)
		}
	}

	// Again: nothing left of the Keşfet set.
	again, err := e.seeder.Purge(ctx, PurgeOptions{})
	if err != nil || len(again.Tayfas) != 0 || again.Accounts != 0 {
		t.Fatalf("ikinci temizlik: %+v %v", again, err)
	}
	if n := e.count(t, userDeleted); n != len(discoverPeople) {
		t.Errorf("ikinci temizlikte user.deleted = %d", n)
	}

	// -hepsi: the closed set too.
	everything, err := e.seeder.Purge(ctx, PurgeOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(everything.Tayfas) != len(tayfas) || everything.Accounts != len(people) {
		t.Fatalf("-hepsi raporu: %+v", everything)
	}
	del = e.deleting()
	for _, ts := range tayfas {
		if !del[ts.Name] {
			t.Errorf("%s -hepsi ile silinmedi", ts.Name)
		}
	}
	if e.fake.tayfas["real-1"].deleting || e.fake.tayfas["real-2"].deleting {
		t.Error("-hepsi gerçek kullanıcının tayfasına dokundu")
	}
	if n := e.count(t, demoIdentities); n != 0 {
		t.Errorf("-hepsi sonrası demo kimliği = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE deleted_at IS NULL`); n != 2 {
		t.Errorf("canlı hesap = %d; yalnızca sahip ve gerçek kullanıcı kalmalı", n)
	}
	logs := e.logs.String()
	for _, secret := range []string{code, "tok-", discoverPeople[0].Name, discoverTayfas[0].Name, "demo-21"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log %q içeriyor", secret)
		}
	}
}

// TestPurgeReadOnlyTayfa: an archived demo tayfa cannot be deleted by its
// founder; it is reported and its accounts still go.
func TestPurgeReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, code := e.owner(t)
	if _, err := e.seeder.Run(ctx, code); err != nil {
		t.Fatal(err)
	}
	for _, ft := range e.fake.tayfas {
		ft.readOnly = true
	}
	rep, err := e.seeder.Purge(ctx, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Tayfas {
		if r.Outcome != PurgeReadOnlySkipped {
			t.Errorf("salt okunur tayfa: %+v", r)
		}
	}
	if rep.Accounts != len(discoverPeople) {
		t.Errorf("hesaplar: %d", rep.Accounts)
	}
}

// TestPurgeBoundary fixes what counts as demo data.
func TestPurgeBoundary(t *testing.T) {
	spec := discoverTayfas[0]
	ok := &corev1.MyTayfa{Name: spec.Name, Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, Kind: corev1.TayfaKind_TAYFA_KIND_STANDARD}
	if !isDemoTayfa(spec, ok) {
		t.Fatal("demo tayfası tanınmadı")
	}
	for name, mt := range map[string]*corev1.MyTayfa{
		"yalnızca üye":   {Name: spec.Name, Role: corev1.Role_ROLE_MODERATOR, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE},
		"bekleyen istek": {Name: spec.Name, Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING},
		"başka ad":       {Name: spec.Name + " 2", Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE},
		"anlık tayfa":    {Name: spec.Name, Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, Kind: corev1.TayfaKind_TAYFA_KIND_INSTANT},
	} {
		if isDemoTayfa(spec, mt) {
			t.Errorf("%s demo tayfası sayıldı", name)
		}
	}
	noDemo := spec
	noDemo.Name = "Yürüyüşçüler"
	if isDemoTayfa(noDemo, &corev1.MyTayfa{Name: "Yürüyüşçüler", Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE}) {
		t.Error(`adında "Demo" olmayan tayfa demo sayıldı`)
	}

	demo := store.GetAuthIdentityRow{Provider: Provider, Subject: "demo-21", Email: pgtype.Text{String: Email("demo-21"), Valid: true}}
	if !isDemoIdentity(demo, "demo-21") {
		t.Fatal("demo kimliği tanınmadı")
	}
	for name, ident := range map[string]store.GetAuthIdentityRow{
		"google":        {Provider: "google", Subject: "demo-21", Email: demo.Email},
		"başka e-posta": {Provider: Provider, Subject: "demo-21", Email: pgtype.Text{String: "demo-21@example.com", Valid: true}},
		"e-posta yok":   {Provider: Provider, Subject: "demo-21"},
		"başka konu":    {Provider: Provider, Subject: "demo-22", Email: demo.Email},
	} {
		if isDemoIdentity(ident, "demo-21") {
			t.Errorf("%s demo kimliği sayıldı", name)
		}
	}
}
