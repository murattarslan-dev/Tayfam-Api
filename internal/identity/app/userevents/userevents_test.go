package userevents

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func inTx(t *testing.T, pool *pgxpool.Pool, fn func(pgx.Tx) error) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), pool, fn); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeletedRemovesPersonalDataFromOutbox(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	ctx := context.Background()
	user, other := id.New(), id.New()
	avatar := id.New().String()
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, user, Profile{DisplayName: "Silinecek Ad", AvatarUploadID: avatar})
	})
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, user, Profile{DisplayName: "Silinecek Yeni Ad"})
	})
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, other, Profile{DisplayName: "Kalan Ad"})
	})

	inTx(t, pool, func(tx pgx.Tx) error { return Deleted(ctx, tx, user) })

	if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Silinecek%' OR payload::text LIKE '%' || $1 || '%'`, avatar); n != 0 {
		t.Fatalf("silinen kullanıcının profili outbox'ta kaldı: %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE actor_id = $1`, user.PG()); n != 0 {
		t.Fatalf("silinen kullanıcı aktör olarak kaldı: %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE type = 'user.deleted' AND subject_id = $1 AND actor_id IS NULL AND payload = '{}'`, user.PG()); n != 1 {
		t.Fatalf("silme olayı = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE type = 'user.profile_updated' AND subject_id = $1`, other.PG()); n != 1 {
		t.Fatal("başka kullanıcının olayı silindi")
	}
}

func TestProfileUpdatedNeedsName(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		return ProfileUpdated(context.Background(), tx, id.New(), Profile{})
	})
	if err == nil {
		t.Fatal("boş ad kabul edildi")
	}
}
