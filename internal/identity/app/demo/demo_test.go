package demo

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/refcode"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

type env struct {
	pool   *pgxpool.Pool
	cipher *birthdate.Cipher
	fake   *fakeServices
	seeder *Seeder
	logs   *bytes.Buffer
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	c, err := birthdate.ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, cipher: c, fake: newFake(), logs: &bytes.Buffer{}}
	e.seeder, err = New(Config{
		Pool: pool, Cipher: c, Log: slog.New(slog.NewJSONHandler(e.logs, nil)),
		Tokens: fakeTokens{}, Core: e.fake.core(), Chat: e.fake,
		Now:   func() time.Time { return now },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// owner registers a real-looking account and returns it with its referral
// code.
func (e *env) owner(t *testing.T) (id.ID, string) {
	t.Helper()
	ctx := context.Background()
	u := id.New()
	var code string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.CreateUser(ctx, store.CreateUserParams{ID: u.PG(), DisplayName: pgtype.Text{String: "Platform Sahibi", Valid: true}, Locale: "tr"}); err != nil {
			return err
		}
		d, _ := birthdate.Parse("1985-01-01")
		if _, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: e.cipher.Encrypt(u, d)}); err != nil {
			return err
		}
		var err error
		code, err = refcode.Assign(ctx, tx, u, false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return u, code
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunWritesDemoDataOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	owner, code := e.owner(t)

	rep, err := e.seeder.Run(ctx, strings.ToLower(code))
	if err != nil {
		t.Fatal(err)
	}
	if rep.UsersCreated != len(people) || rep.UsersExisting != 0 || len(rep.Tayfas) != len(tayfas) {
		t.Fatalf("rapor = %+v", rep)
	}

	// identity: registered demo accounts with the marker e-mail, an adult
	// encrypted birth date, a referral code and one profile event each.
	if n := e.count(t, `SELECT count(*) FROM auth_identities WHERE provider = 'demo' AND email LIKE '%@demo.tayfam.app'`); n != len(people) {
		t.Errorf("demo kimliği = %d", n)
	}
	rows, err := e.pool.Query(ctx, `
		SELECT u.id, u.birth_date_enc, u.registered_at IS NOT NULL, u.ref_code IS NOT NULL
		FROM users u JOIN auth_identities ai ON ai.user_id = u.id WHERE ai.provider = 'demo'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var (
			u          pgtype.UUID
			enc        []byte
			reg, hasRC bool
		)
		if err := rows.Scan(&u, &enc, &reg, &hasRC); err != nil {
			t.Fatal(err)
		}
		uid, _ := id.FromPG(u)
		d, err := e.cipher.Decrypt(uid, enc)
		if err != nil || d.IsMinor(now) || !reg || !hasRC {
			t.Errorf("demo hesabı %s: yetişkin/kayıtlı/kodlu değil (%v)", uid, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM consent_records`); n != 0 {
		t.Errorf("demo hesapları adına onay kaydı yazıldı: %d", n)
	}
	profileEvents := `SELECT count(*) FROM domain_events WHERE type = 'user.profile_updated'`
	if n := e.count(t, profileEvents); n != len(people) {
		t.Errorf("profil olayı = %d", n)
	}

	// core and chat: the owner's roles, every event type, the content.
	got := e.fake.snapshot()
	wantRoles := map[string]corev1.Role{}
	for _, ts := range tayfas {
		wantRoles[ts.Name] = ts.OwnerRole
	}
	for _, ft := range e.fake.tayfas {
		if !strings.Contains(ft.name, "Demo") {
			t.Errorf("tayfa adında Demo yok: %s", ft.name)
		}
		if r := ft.roles[owner.String()]; r != wantRoles[ft.name] {
			t.Errorf("%s: sahibin rolü %v, beklenen %v", ft.name, r, wantRoles[ft.name])
		}
	}
	types := map[corev1.EventType]bool{}
	for _, ev := range e.fake.events {
		types[ev.ev.GetType()] = true
	}
	for v := range corev1.EventType_name {
		if typ := corev1.EventType(v); typ != corev1.EventType_EVENT_TYPE_UNSPECIFIED && !types[typ] {
			t.Errorf("etkinlik türü yok: %v", typ)
		}
	}
	var anonymous, open int
	for _, p := range e.fake.polls {
		if p.poll.GetAnonymous() {
			anonymous++
		} else {
			open++
		}
		if len(p.votes) == 0 {
			t.Errorf("oysuz anket: %s", p.poll.GetQuestion())
		}
	}
	if anonymous == 0 || open == 0 {
		t.Errorf("gizli %d, açık %d anket", anonymous, open)
	}
	for _, k := range []string{"pinned", "answer", "answer_votes", "vehicles", "items", "message", "channel", "rsvps"} {
		if got[k] == 0 {
			t.Errorf("%s yok: %v", k, got)
		}
	}
	if e.fake.plates != 0 {
		t.Errorf("plakalı araç yazıldı: %d", e.fake.plates)
	}
	if got["pending"] != 0 {
		t.Errorf("onay bekleyen kaldı: %d", got["pending"])
	}
	// The owner's token is used only to ask to join.
	for _, c := range e.fake.calls {
		if c.Caller == owner.String() && c.Method != "JoinByCode" {
			t.Errorf("sahibin hesabıyla %s çağrıldı", c.Method)
		}
	}
	// No code, name or text in the logs.
	logs := e.logs.String()
	for _, secret := range []string{code, "tok-", people[0].Name, tayfas[0].Name, "demo-01"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log %q içeriyor", secret)
		}
	}

	// A second run finds everything and creates nothing.
	callsBefore := len(e.fake.calls)
	rep2, err := e.seeder.Run(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.UsersCreated != 0 || rep2.UsersExisting != len(people) {
		t.Fatalf("ikinci rapor = %+v", rep2)
	}
	for i := range rep.Tayfas {
		if rep.Tayfas[i].ID != rep2.Tayfas[i].ID {
			t.Errorf("tayfa yeniden oluşturuldu: %s → %s", rep.Tayfas[i].ID, rep2.Tayfas[i].ID)
		}
	}
	if again := e.fake.snapshot(); !maps.Equal(got, again) {
		t.Errorf("ikinci çalıştırma veri değiştirdi:\nilk   %v\nikinci %v", got, again)
	}
	if n := e.count(t, profileEvents); n != len(people) {
		t.Errorf("ikinci çalıştırmada profil olayı = %d", n)
	}
	for _, c := range e.fake.calls[callsBefore:] {
		if c.Caller == owner.String() {
			t.Errorf("ikinci çalıştırmada sahibin hesabıyla %s çağrıldı", c.Method)
		}
	}
}

func TestUnknownOwnerWritesNothing(t *testing.T) {
	e := setup(t)
	for _, code := range []string{"TYF-ZZZZZZ", "", "bozuk kod"} {
		if _, err := e.seeder.Run(context.Background(), code); !errors.Is(err, ErrOwnerNotFound) {
			t.Errorf("%q: %v", code, err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM users`); n != 0 {
		t.Errorf("kullanıcı yazıldı: %d", n)
	}
	if len(e.fake.calls) != 0 {
		t.Errorf("servis çağrıldı: %v", e.fake.calls)
	}
}

func TestDemoAccountIsNotOwner(t *testing.T) {
	e := setup(t)
	_, code := e.owner(t)
	if _, err := e.seeder.Run(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	var demoCode string
	if err := e.pool.QueryRow(context.Background(), `
		SELECT u.ref_code FROM users u JOIN auth_identities ai ON ai.user_id = u.id
		WHERE ai.provider = 'demo' AND ai.subject = 'demo-05'`).Scan(&demoCode); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seeder.Run(context.Background(), demoCode); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("demo hesabı sahip olarak kabul edildi: %v", err)
	}
}

func TestReadOnlyTayfaIsSkipped(t *testing.T) {
	e := setup(t)
	_, code := e.owner(t)
	ctx := context.Background()
	if _, err := e.seeder.Run(ctx, code); err != nil {
		t.Fatal(err)
	}
	for _, ft := range e.fake.tayfas {
		ft.readOnly = true // archived by the inactive tayfa cleanup
	}
	rep, err := e.seeder.Run(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Tayfas {
		if r.ID == "" || !r.ReadOnly {
			t.Errorf("salt okunur tayfa atlanmadı: %+v", r)
		}
	}
}

func TestRequestID(t *testing.T) {
	a, b := requestID("ege/tayfa"), requestID("ege/tayfa")
	if a != b {
		t.Fatalf("aynı anahtar farklı kimlik: %s %s", a, b)
	}
	if requestID("nif/tayfa") == a {
		t.Fatal("farklı anahtar aynı kimlik")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("UUIDv8 değil: %s", a)
	}
	if _, ok := id.Parse(a); !ok {
		t.Fatalf("ayrıştırılamadı: %s", a)
	}
}

func TestStartTime(t *testing.T) {
	s := &Seeder{cfg: Config{Now: func() time.Time { return time.Date(2026, 11, 13, 22, 30, 0, 0, time.UTC) }}}
	// 22:30 UTC is already 14 November in Turkey.
	got := s.startTime(at{Days: 1, Hour: 8, Minute: 30})
	want := time.Date(2026, 11, 15, 5, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("startTime = %s, beklenen %s", got.UTC(), want)
	}
}

// The data set follows the platform owner's decisions (issue #179) and the
// API's limits, so a run never stops halfway on a validation error.
func TestDataSet(t *testing.T) {
	if n := len(people); n < 15 || n > 20 {
		t.Errorf("%d demo hesabı; 15–20 olmalı", n)
	}
	keys, subjects := map[string]bool{}, map[string]bool{}
	subject := regexp.MustCompile(`^demo-[0-9]{2}$`)
	for _, p := range people {
		if keys[p.Key] || subjects[p.Subject] {
			t.Errorf("tekrarlanan hesap: %s", p.Key)
		}
		keys[p.Key], subjects[p.Subject] = true, true
		if !subject.MatchString(p.Subject) {
			t.Errorf("%s: subject %q", p.Key, p.Subject)
		}
		if n := utf8.RuneCountInString(p.Name); n < 1 || n > 40 {
			t.Errorf("%s: ad uzunluğu %d", p.Key, n)
		}
		d, err := birthdate.Parse(p.BirthDate)
		if err != nil || d.IsMinor(time.Now()) {
			t.Errorf("%s: doğum tarihi yetişkin değil (%v)", p.Key, err)
		}
	}

	roles := map[corev1.Role]bool{}
	types := map[corev1.EventType]bool{}
	for _, ts := range tayfas {
		if !strings.Contains(ts.Name, "Demo") {
			t.Errorf("%s: adda Demo yok", ts.Key)
		}
		if n := utf8.RuneCountInString(ts.Name); n < 3 || n > 50 {
			t.Errorf("%s: ad uzunluğu %d", ts.Key, n)
		}
		if utf8.RuneCountInString(ts.Description) > 500 {
			t.Errorf("%s: açıklama uzun", ts.Key)
		}
		roles[ts.OwnerRole] = true
		in := map[string]corev1.Role{ts.Founder: corev1.Role_ROLE_ADMIN}
		if !keys[ts.Founder] {
			t.Errorf("%s: kurucu yok", ts.Key)
		}
		for _, m := range ts.Members {
			if _, dup := in[m.Person]; dup || !keys[m.Person] {
				t.Errorf("%s: üye %s tekrar ya da yok", ts.Key, m.Person)
			}
			if m.Role != corev1.Role_ROLE_MEMBER && m.Role != corev1.Role_ROLE_MODERATOR {
				t.Errorf("%s: %s rolü %v", ts.Key, m.Person, m.Role)
			}
			in[m.Person] = m.Role
		}
		isIn := func(where, k string) {
			if _, ok := in[k]; !ok {
				t.Errorf("%s: %s %s tayfada değil", ts.Key, where, k)
			}
		}
		for _, ev := range ts.Events {
			types[ev.Type] = true
			if ev.Hours < 1 || ev.Hours > 30*24 || ev.Start.Days < 1 {
				t.Errorf("%s/%s: zaman", ts.Key, ev.Key)
			}
			if ev.Online != (ev.OnlineURL != "") || (ev.Online && ev.City != "") || (!ev.Online && (ev.City == "" || ev.District == "")) {
				t.Errorf("%s/%s: yer bilgisi", ts.Key, ev.Key)
			}
			going := map[string]bool{}
			for _, r := range ev.RSVPs {
				isIn("katılımcı", r.Person)
				if r.Status == corev1.RsvpStatus_RSVP_STATUS_GOING {
					going[r.Person] = true
				}
			}
			for _, v := range ev.Vehicles {
				if !going[v.Person] {
					t.Errorf("%s/%s: %s gelmiyor ama aracı var", ts.Key, ev.Key, v.Person)
				}
			}
			for _, it := range ev.CampItems {
				if it.Assigned != "" {
					isIn("malzeme", it.Assigned)
				}
			}
		}
		pinned := false
		for _, n := range ts.Notices {
			pinned = pinned || n.Pinned
		}
		if !pinned {
			t.Errorf("%s: sabit duyuru yok", ts.Key)
		}
		for _, q := range ts.Questions {
			isIn("soru", q.Author)
			if n := utf8.RuneCountInString(q.Title); n < 10 || n > 150 {
				t.Errorf("%s/%s: soru başlığı %d", ts.Key, q.Key, n)
			}
			for _, a := range q.Answers {
				isIn("cevap", a.Author)
				for _, v := range a.Voters {
					isIn("oy", v)
					if v == a.Author {
						t.Errorf("%s/%s: kendi cevabına oy", ts.Key, q.Key)
					}
				}
			}
		}
		for _, p := range ts.Polls {
			isIn("anket", p.Author)
			if len(p.Options) < 2 || len(p.Options) > 10 {
				t.Errorf("%s/%s: seçenek sayısı", ts.Key, p.Key)
			}
			for _, v := range p.Votes {
				isIn("anket oyu", v.Person)
				if len(v.Options) == 0 || (!p.Multiple && len(v.Options) != 1) {
					t.Errorf("%s/%s: %s oyu", ts.Key, p.Key, v.Person)
				}
				for _, i := range v.Options {
					if i < 0 || i >= len(p.Options) {
						t.Errorf("%s/%s: seçenek %d", ts.Key, p.Key, i)
					}
				}
			}
		}
		for _, c := range ts.Channels {
			mk := map[string]bool{}
			for _, m := range c.Messages {
				isIn("mesaj", m.Author)
				if c.Staff && in[m.Author] < corev1.Role_ROLE_MODERATOR {
					t.Errorf("%s/%s: %s yönetim kanalına yazamaz", ts.Key, c.Name, m.Author)
				}
				if mk[m.Key] {
					t.Errorf("%s/%s: mesaj anahtarı tekrar", ts.Key, c.Name)
				}
				mk[m.Key] = true
			}
		}
	}
	for _, r := range []corev1.Role{corev1.Role_ROLE_MEMBER, corev1.Role_ROLE_MODERATOR, corev1.Role_ROLE_ADMIN} {
		if !roles[r] {
			t.Errorf("platform sahibi hiçbir tayfada %v değil", r)
		}
	}
	if n := len(tayfas); n < 3 || n > 4 {
		t.Errorf("%d demo tayfası; 3–4 olmalı", n)
	}
	for v := range corev1.EventType_name {
		if typ := corev1.EventType(v); typ != corev1.EventType_EVENT_TYPE_UNSPECIFIED && !types[typ] {
			t.Errorf("etkinlik türü yok: %v", typ)
		}
	}
}
