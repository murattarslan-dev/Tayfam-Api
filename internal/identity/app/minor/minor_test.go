package minor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// now is 12:00 UTC (15:00 in Istanbul) on 8 November 2026.
var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestIsMinor(t *testing.T) {
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	q := store.New(pool)
	c, err := birthdate.ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)))
	must(t, err)
	svc, err := New(Config{Pool: pool, Cipher: c, Now: func() time.Time { return now }})
	must(t, err)

	user := func(date string) id.ID {
		t.Helper()
		u := id.New()
		must(t, q.CreateUser(ctx, store.CreateUserParams{ID: u.PG(), DisplayName: pgtype.Text{String: "Deniz", Valid: true}, Locale: "tr"}))
		if date != "" {
			d, err := birthdate.Parse(date)
			must(t, err)
			n, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: c.Encrypt(u, d)})
			must(t, err)
			if n != 1 {
				t.Fatal("kayıt tamamlanmadı")
			}
		}
		return u
	}
	adult := user("2008-11-08")    // 18 today
	minor := user("2008-11-09")    // 18 tomorrow
	young := user("2010-01-01")    // 16
	pending := user("")            // registration not completed
	unknown := id.New()            // no such user
	deleted := user("1990-01-01")  // deleted below
	tampered := user("1990-01-01") // value copied from another row below
	_, err = pool.Exec(ctx, `UPDATE users SET deleted_at = now(), birth_date_enc = NULL WHERE id = $1`, deleted.PG())
	must(t, err)
	_, err = pool.Exec(ctx, `UPDATE users SET birth_date_enc = (SELECT birth_date_enc FROM users WHERE id = $1) WHERE id = $2`, young.PG(), tampered.PG())
	must(t, err)

	got, err := svc.IsMinor(ctx, []id.ID{adult, minor, young, pending, unknown, deleted, tampered})
	must(t, err)
	want := map[id.ID]bool{adult: false, minor: true, young: true}
	if len(got) != len(want) {
		t.Fatalf("cevaplar = %v", got)
	}
	for u, m := range want {
		if v, ok := got[u]; !ok || v != m {
			t.Errorf("%s: %v %v, %v bekleniyordu", u, v, ok, m)
		}
	}
	// Another row's value does not decrypt (AAD): no answer, never its
	// owner's answer.
	if _, ok := got[tampered]; ok {
		t.Fatal("kopyalanmış şifreli değer cevaplandı")
	}

	for _, n := range []int{0, MaxUsers + 1} {
		users := make([]id.ID, n)
		for i := range users {
			users[i] = id.New()
		}
		if _, err := svc.IsMinor(ctx, users); !errors.Is(err, ErrTooMany) {
			t.Errorf("%d kullanıcı: %v", n, err)
		}
	}
}
