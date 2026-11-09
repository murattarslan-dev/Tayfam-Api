package consent

import (
	"bytes"
	"context"
	"errors"
	"testing"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestReconsentForNewVersion(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	ctx := context.Background()
	old := Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}
	cur := Documents{PrivacyNotice: "2026-12-01", Terms: "2026-11-08"}
	svc, err := NewService(pool, cur)
	if err != nil {
		t.Fatal(err)
	}
	user, pending := id.New(), id.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, display_name, birth_date_enc, registered_at) VALUES ($1, 'Kayıtlı', $2, now())`, user.PG(), bytes.Repeat([]byte{1}, 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, display_name) VALUES ($1, 'Bekleyen')`, pending.PG()); err != nil {
		t.Fatal(err)
	}
	if err := pgxRecord(ctx, svc, user, old); err != nil {
		t.Fatal(err)
	}

	st, err := svc.Status(ctx, user)
	if err != nil || !st.Required || st.Documents != cur {
		t.Fatalf("durum = %+v, %v", st, err)
	}
	if err := svc.Accept(ctx, user, old.PrivacyNotice, old.Terms); !errors.Is(err, ErrOutdated) {
		t.Fatalf("eski sürüm onayı: %v", err)
	}
	if err := svc.Accept(ctx, user, cur.PrivacyNotice, cur.Terms); err != nil {
		t.Fatal(err)
	}
	// Accepting again adds nothing.
	if err := svc.Accept(ctx, user, cur.PrivacyNotice, cur.Terms); err != nil {
		t.Fatal(err)
	}
	if st, err = svc.Status(ctx, user); err != nil || st.Required {
		t.Fatalf("onaydan sonra durum = %+v, %v", st, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consent_records WHERE user_id = $1`, user.PG()).Scan(&n); err != nil || n != 3 {
		t.Fatalf("onay kayıtları = %d (eski iki sürüm kalır, yeni aydınlatma metni eklenir), %v", n, err)
	}

	for name, u := range map[string]id.ID{"kayıt bekleyen": pending, "bilinmeyen": id.New()} {
		if err := svc.Accept(ctx, u, cur.PrivacyNotice, cur.Terms); !errors.Is(err, ErrUnknownUser) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := svc.Status(ctx, u); !errors.Is(err, ErrUnknownUser) {
			t.Errorf("%s durum: %v", name, err)
		}
	}
}

func pgxRecord(ctx context.Context, s *Service, user id.ID, d Documents) error {
	return Record(ctx, s.q, user, d)
}
