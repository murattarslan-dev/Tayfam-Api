package registration

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var (
	now  = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	docs = Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}
)

type env struct {
	svc    *Service
	pool   *pgxpool.Pool
	q      *store.Queries
	cipher *birthdate.Cipher
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, identitydb.Migrations())
	c, err := birthdate.ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Cipher: c, Documents: docs, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool, q: store.New(pool), cipher: c}
}

// newUser creates a signed-in but unregistered user with one session.
func (e *env) newUser(t *testing.T) id.ID {
	t.Helper()
	ctx := context.Background()
	u, dev, sess := id.New(), id.New(), id.New()
	must(t, e.q.CreateUser(ctx, store.CreateUserParams{ID: u.PG(), DisplayName: pgtype.Text{String: "Google Adı", Valid: true}, Locale: "tr"}))
	must(t, e.q.CreateAuthIdentity(ctx, store.CreateAuthIdentityParams{ID: id.New().PG(), UserID: u.PG(), Provider: "google", Subject: "sub-" + u.String(), Email: pgtype.Text{String: "genc@example.com", Valid: true}}))
	must(t, e.q.CreateDevice(ctx, store.CreateDeviceParams{ID: dev.PG(), UserID: u.PG(), Platform: "android"}))
	must(t, e.q.CreateSession(ctx, store.CreateSessionParams{ID: sess.PG(), UserID: u.PG(), DeviceID: dev.PG(), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}))
	return u
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func input(date string) Input {
	return Input{BirthDate: date, PrivacyNoticeVersion: docs.PrivacyNotice, TermsVersion: docs.Terms}
}

func TestCompleteRegistration(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u := e.newUser(t)
	in := input("2010-11-08") // bugün 16
	in.DisplayName = "  Ayşe   Işık "
	if err := e.svc.Complete(ctx, u, in); err != nil {
		t.Fatal(err)
	}
	got, err := e.q.GetUser(ctx, u.PG())
	must(t, err)
	if !got.RegisteredAt.Valid || got.DisplayName.String != "Ayşe Işık" {
		t.Fatalf("kullanıcı = %+v", got)
	}
	if !regexp.MustCompile(`^TYF-[2-9A-HJ-NP-Z]{6}$`).MatchString(got.RefCode.String) {
		t.Fatalf("referans kodu = %q", got.RefCode.String)
	}
	if bytes.Contains(got.BirthDateEnc, []byte("2010")) {
		t.Fatal("doğum tarihi açık metin olarak saklanmış")
	}
	d, err := e.cipher.Decrypt(u, got.BirthDateEnc)
	if err != nil || d.String() != "2010-11-08" {
		t.Fatalf("doğum tarihi = %v %v", d, err)
	}
	for _, typ := range []string{"privacy_notice", "terms"} {
		ok, err := e.q.HasActiveConsent(ctx, store.HasActiveConsentParams{UserID: u.PG(), DocumentType: typ, DocumentVersion: docs.PrivacyNotice})
		if err != nil || !ok {
			t.Fatalf("%s onayı yok: %v", typ, err)
		}
	}
	if err := e.svc.Complete(ctx, u, input("1990-01-01")); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("ikinci kayıt: %v", err)
	}
	// The profile copy event: name only, no birth date, e-mail or ref code.
	if got := e.profileEvents(t, u); len(got) != 1 || got[0] != `{"display_name": "Ayşe Işık", "activity_visibility": "tayfa"}` {
		t.Fatalf("profil olayları = %q", got)
	}
}

// profileEvents returns the payloads of user.profile_updated events for u.
func (e *env) profileEvents(t *testing.T, u id.ID) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT payload::text FROM domain_events WHERE type = 'user.profile_updated' AND subject_type = 'user' AND subject_id = $1 AND actor_id = $1 ORDER BY id`, u.PG())
	must(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		must(t, rows.Scan(&p))
		out = append(out, p)
	}
	must(t, rows.Err())
	return out
}

func TestUnderAgeIsBlockedForGood(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u := e.newUser(t)
	if err := e.svc.Complete(ctx, u, input("2010-11-09")); !errors.Is(err, ErrUnderAge) { // yarın 16
		t.Fatalf("err = %v", err)
	}
	got, err := e.q.GetUser(ctx, u.PG())
	must(t, err)
	if !got.AgeBlockedAt.Valid || got.RegisteredAt.Valid || got.BirthDateEnc != nil {
		t.Fatalf("engellenen hesap: %+v", got)
	}
	var emails, active int
	must(t, e.pool.QueryRow(ctx, `SELECT count(email) FROM auth_identities WHERE user_id = $1`, u.PG()).Scan(&emails))
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, u.PG()).Scan(&active))
	if emails != 0 || active != 0 {
		t.Fatalf("engellenen hesapta e-posta=%d açık oturum=%d", emails, active)
	}
	if got := e.profileEvents(t, u); len(got) != 0 {
		t.Fatalf("reddedilen kayıt profil olayı yazdı: %q", got)
	}
	// Başka bir tarihle tekrar denemek işe yaramaz.
	if err := e.svc.Complete(ctx, u, input("1990-01-01")); !errors.Is(err, ErrAgeBlocked) {
		t.Fatalf("tekrar deneme: %v", err)
	}
}

func TestRejectsBadInput(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u := e.newUser(t)
	bad := func(mod func(*Input)) Input { in := input("1990-05-19"); mod(&in); return in }
	for name, tc := range map[string]struct {
		in   Input
		want error
	}{
		"bozuk tarih":        {input("1990-02-30"), ErrInvalidBirthDate},
		"gelecek":            {input("2027-01-01"), ErrInvalidBirthDate},
		"1900 öncesi":        {input("1899-12-31"), ErrInvalidBirthDate},
		"eski aydınlatma":    {bad(func(i *Input) { i.PrivacyNoticeVersion = "2026-01-01" }), ErrConsentOutdated},
		"koşullar onaysız":   {bad(func(i *Input) { i.TermsVersion = "" }), ErrConsentOutdated},
		"41 karakter ad":     {bad(func(i *Input) { i.DisplayName = string(bytes.Repeat([]byte("a"), 41)) }), ErrInvalidDisplayName},
		"kontrol karakterli": {bad(func(i *Input) { i.DisplayName = "Ali\u0007" }), ErrInvalidDisplayName},
	} {
		if err := e.svc.Complete(ctx, u, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, %v bekleniyordu", name, err, tc.want)
		}
	}
	// Hiçbiri kalıcı bir değişiklik bırakmadı.
	got, _ := e.q.GetUser(ctx, u.PG())
	if got.RegisteredAt.Valid || got.AgeBlockedAt.Valid || len(e.profileEvents(t, u)) != 0 {
		t.Fatal("geçersiz girdi hesabı değiştirdi")
	}
	if err := e.svc.Complete(ctx, id.New(), input("1990-05-19")); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("bilinmeyen kullanıcı: %v", err)
	}
}

func TestEmptyDisplayNameKeepsProviderName(t *testing.T) {
	e := setup(t)
	u := e.newUser(t)
	must(t, e.svc.Complete(context.Background(), u, input("1990-05-19")))
	got, _ := e.q.GetUser(context.Background(), u.PG())
	if got.DisplayName.String != "Google Adı" {
		t.Fatalf("ad = %q", got.DisplayName.String)
	}
	if got := e.profileEvents(t, u); len(got) != 1 || got[0] != `{"display_name": "Google Adı", "activity_visibility": "tayfa"}` {
		t.Fatalf("profil olayları = %q", got)
	}
}

func TestReferralCodeAtRegistration(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ref := e.newUser(t)
	must(t, e.svc.Complete(ctx, ref, input("1990-05-19")))
	refUser, err := e.q.GetUser(ctx, ref.PG())
	must(t, err)
	code := refUser.RefCode.String

	referredBy := func(u id.ID) (pgtype.UUID, pgtype.Timestamptz) {
		t.Helper()
		var by pgtype.UUID
		var at pgtype.Timestamptz
		must(t, e.pool.QueryRow(ctx, `SELECT referred_by_user_id, referred_at FROM users WHERE id = $1`, u.PG()).Scan(&by, &at))
		return by, at
	}

	// Lower case, without the prefix: it is the same code.
	u := e.newUser(t)
	in := input("1990-05-19")
	in.ReferralCode = " " + strings.ToLower(strings.TrimPrefix(code, "TYF-")) + " "
	must(t, e.svc.Complete(ctx, u, in))
	if by, at := referredBy(u); id.ID(by.Bytes) != ref || !at.Valid {
		t.Fatalf("referans = %v %v", by, at)
	}
	// Once set it never changes, not even straight in the database.
	other := e.newUser(t)
	must(t, e.svc.Complete(ctx, other, input("1990-05-19")))
	if _, err := e.pool.Exec(ctx, `UPDATE users SET referred_by_user_id = $2 WHERE id = $1`, u.PG(), other.PG()); err == nil {
		t.Fatal("referans değiştirildi")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET referred_at = NULL, referred_by_user_id = NULL WHERE id = $1`, u.PG()); err == nil {
		t.Fatal("referans silinip yeniden yazılabilir")
	}

	// Unknown and malformed codes are ignored; registration still succeeds
	// and tells nothing about the code.
	for _, c := range []string{"TYF-ZZZZZZ", "bozuk kod", strings.Repeat("A", 20)} {
		u := e.newUser(t)
		in := input("1990-05-19")
		in.ReferralCode = c
		if err := e.svc.Complete(ctx, u, in); err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if by, at := referredBy(u); by.Valid || at.Valid {
			t.Fatalf("%q: referans yazıldı", c)
		}
	}
}
